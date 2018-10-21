FROM fnproject/go:dev as build-stage

#RUN curl https://raw.githubusercontent.com/golang/dep/master/install.sh | sh

# Set go bin which doesn't appear to be set already.
ENV GOBIN /go/bin

# build directories
RUN mkdir /app
RUN mkdir /go/src/app
ADD . /go/src/app
WORKDIR /go/src/app

# Build
RUN go get -u github.com/golang/dep/...
RUN dep ensure
RUN go build -o pollster .


# Get Binary
FROM fnproject/go
COPY --from=build-stage /go/src/app /
ENTRYPOINT ["/pollster"]
