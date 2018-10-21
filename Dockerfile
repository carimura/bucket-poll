FROM fnproject/go:dev as build-stage

# Set go bin which doesn't appear to be set already.
ENV GOBIN /go/bin

# Build
ADD . /go/src/app/vendor/github.com/carimura/bucket-poll
RUN cd /go/src/app/vendor/github.com/carimura/bucket-poll; go build -o pollster .

# Get Binary
FROM fnproject/go
COPY --from=build-stage /go/src/app/vendor/github.com/carimura/bucket-poll /
ENTRYPOINT ["/pollster"]
