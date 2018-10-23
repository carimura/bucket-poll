package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
	"github.com/carimura/bucket-poll/common"
	"github.com/sirupsen/logrus"
)

type Store struct {
	Client     *s3.S3
	Uploader   *s3manager.Uploader
	Downloader *s3manager.Downloader
	bucket     string
	Config     *MinioConfig
}

func (m *MinioConfig) createStore() *Store {
	log.Println(m.AccessKeyID)
	log.Println(m.SecretAccessKey)
	log.Println(m.Region)
	log.Println(m.Endpoint)

	client := s3.New(session.Must(session.NewSession(&aws.Config{
		Credentials:      credentials.NewStaticCredentials(m.AccessKeyID, m.SecretAccessKey, ""),
		Endpoint:         aws.String(m.Endpoint),
		Region:           aws.String(m.Region),
		DisableSSL:       aws.Bool(!m.UseSSL),
		S3ForcePathStyle: aws.Bool(true),
	})))
	return &Store{
		Client:     client,
		Config:     m,
		Uploader:   s3manager.NewUploaderWithClient(client),
		Downloader: s3manager.NewDownloaderWithClient(client),
	}
}

type MinioConfig struct {
	Bucket          string `json:"bucket"`
	Endpoint        string `json:"endpoint"`
	Region          string `json:"region"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	UseSSL          bool   `json:"use_ssl"`
	RawEndpoint     string `json:"raw_endpoint"`
}

func GetStore() (*Store, error) {
	m := &MinioConfig{}
	m.Bucket = os.Getenv("BUCKET")
	m.Region = os.Getenv("REGION")
	m.AccessKeyID = os.Getenv("ACCESS_KEY_ID")
	m.SecretAccessKey = os.Getenv("SECRET_ACCESS_KEY")
	m.Endpoint = os.Getenv("STORAGE_URL")
	m.UseSSL = true
	store := m.createStore()
	log.Println("MinioConfig So Far: ", m)

	_, err := store.Client.CreateBucket(&s3.CreateBucketInput{Bucket: aws.String(m.Bucket)})
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			case s3.ErrCodeBucketAlreadyOwnedByYou, s3.ErrCodeBucketAlreadyExists:
				// bucket already exists, NO-OP
			default:
				return nil, fmt.Errorf("failed to create bucket %s: %s", m.Bucket, aerr.Message())
			}
		} else {
			return nil, fmt.Errorf("unexpected error creating bucket %s: %s", m.Bucket, err.Error())
		}
	}
	log.Println("Returning Store!")
	return store, nil
}

func diffSlices(a, b []string) []string {
	mb := map[string]bool{}
	for _, x := range b {
		mb[x] = true
	}
	var ab []string
	for _, x := range a {
		if _, ok := mb[x]; !ok {
			ab = append(ab, x)
		}
	}

	return ab
}

func getObjFromResult(key string, res *s3.ListObjectsV2Output) *s3.Object {
	for _, b := range res.Contents {
		if key == *b.Key {
			return b
		}
	}
	return nil
}

func (s *Store) asyncDispatcher(ctx context.Context, log *logrus.Entry, input *s3.ListObjectsV2Input, req *http.Request, httpClient *http.Client, previousKeys []string) ([]string, error) {

	result, err := s.Client.ListObjectsV2WithContext(ctx, input)
	if err != nil {
		return nil, err
	}
	log.Println("Response from bucket: ", len(result.Contents), "objects")
	log.Println("Full Response: ", result)
	fields := logrus.Fields{}
	fields["objects_found"] = len(result.Contents)

	log = log.WithFields(fields)
	var b bytes.Buffer
	var newKeys []string
	if len(result.Contents) > 0 {
		for _, object := range result.Contents {
			newKeys = append(newKeys, *object.Key)
		}
		for _, objectKey := range diffSlices(newKeys, previousKeys) {
			go func(object *s3.Object) {
				err := func() error {
					log.Info("Sending the object now: ", s.Config.Bucket+"/"+*object.Key)
					getR, _ := s.Client.GetObjectRequest(&s3.GetObjectInput{
						Bucket: aws.String(s.Config.Bucket),
						Key:    object.Key,
					})
					getRstr, err := getR.Presign(1 * time.Hour)
					if err != nil {
						return err
					}

					putR, _ := s.Client.PutObjectRequest(&s3.PutObjectInput{
						Bucket: aws.String(s.Config.Bucket),
						Key:    object.Key,
					})
					putRstr, err := putR.Presign(1 * time.Hour)
					if err != nil {
						return err
					}
					deleteR, _ := s.Client.DeleteObjectRequest(&s3.DeleteObjectInput{
						Bucket: aws.String(s.Config.Bucket),
						Key:    object.Key,
					})
					deleteRstr, err := deleteR.Presign(1 * time.Hour)
					if err != nil {
						return err
					}
					payload := &common.RequestPayload{
						S3Endpoint: s.Config.RawEndpoint,
						Bucket:     s.Config.Bucket,
						Object:     *object.Key,
						PreSignedURLs: common.PreSignedURLs{
							GetURL:    getRstr,
							PutURL:    putRstr,
							DeleteURL: deleteRstr,
						},
					}
					b.Reset()
					err = json.NewEncoder(&b).Encode(&payload)
					if err != nil {
						return err
					}

					req.Body = ioutil.NopCloser(&b)
					err = common.DoRequest(req, httpClient)
					if err != nil {
						return err
					}

					return nil
				}()
				if err != nil {
					log.Error(err.Error())
				}

			}(getObjFromResult(objectKey, result))
		}
	}

	for _, k := range previousKeys {
		s.Client.DeleteObject(&s3.DeleteObjectInput{
			Bucket: aws.String(s.Config.Bucket),
			Key:    aws.String(k),
		})
	}

	return diffSlices(newKeys, previousKeys), nil
}

func (s *Store) DispatchObjects(ctx context.Context) error {
	log := logrus.WithFields(logrus.Fields{"bucketName": s.Config.Bucket})

	input := &s3.ListObjectsV2Input{
		Bucket:  aws.String(s.Config.Bucket),
		MaxKeys: aws.Int64(1000),
	}
	webkookEndpoint := os.Getenv("WEBHOOK_ENDPOINT")
	if webkookEndpoint == "" {
		return errors.New("WEBHOOK_ENDPOINT is not set")
	}

	_, err := url.Parse(webkookEndpoint)
	if err != nil {
		return fmt.Errorf("invalid webhook URL: %s", err.Error())
	}

	req, err := http.NewRequest(http.MethodPost, webkookEndpoint, nil)
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	httpClient := common.SetupHTTPClient()

	backoff := common.WithDefault("POLLSTER_BACKOFF", "5")
	intBackoff, _ := strconv.Atoi(backoff)
	var keys []string
	for {

		keys, err = s.asyncDispatcher(ctx, log, input, req, httpClient, keys)
		if err != nil {
			log.Error(err.Error())
		}

		time.Sleep(time.Duration(intBackoff) * time.Second)
	}

	return nil
}
