ORG=openinsight-proj
PROJECT=etcd-metrics-proxy
REG=quay.io
TAG=0.6.0
PKG=github.com/openinsight-proj/etcd-metrics-proxy

GOARCH ?= $(shell go env GOARCH)
BUILD_ARCH ?= linux/$(GOARCH)

check:
	go vet .
.PHONY: check

test:
	go test -v . -cover -race -p=1
.PHONY: test

tidy:
	go mod tidy
.PHONY: tidy

build: tidy
	GOOS=linux GOARCH=${GOARCH} go build -a --ldflags '-extldflags "-static"' -tags netgo -installsuffix netgo -o etcd-metrics-proxy .
.PHONY: build

.PHONY: image/build
image/build: build
	docker build -t ${REG}/${ORG}/${PROJECT}:${TAG} .

# Build and push a multi-architecture docker image
.PHONY: image/buildx
image/buildx: build
	docker buildx build --platform linux/amd64,linux/arm64,linux/s390x,linux/ppc64le --push -t ${REG}/${ORG}/${PROJECT}:${TAG} .

.PHONY: image/push
image/push:
	docker push ${REG}/${ORG}/${PROJECT}:${TAG}
