package main

import (
	"context"
	"fmt"
	"os"

	"github.com/carimura/bucket-poll/api"
)

func start() error {
	ctx := context.Background()
	s3, err := api.GetStore()
	if err != nil {
		return err
	}

	return s3.DispatchObjects(ctx)
}

func main() {
	err := start()
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}
