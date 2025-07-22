BINARY_NAME=device-plugin
IMAGE_NAME=quay.io/mresvani/shared-nvswitch-gpu-device-plugin
TAG?=latest

.PHONY: build test clean docker-build docker-push help

help:
	@echo "Available targets:"
	@echo "  build       - Build the binary"
	@echo "  test        - Run tests"
	@echo "  test-cover  - Run tests with coverage"
	@echo "  clean       - Clean build artifacts"
	@echo "  image-build - Build Docker image"
	@echo "  image-push  - Push Docker image"
	@echo "  fmt         - Format code"
	@echo "  lint        - Run linter"

build:
	CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o $(BINARY_NAME) ./cmd/device-plugin/

test:
	go test -v ./...

test-cover:
	go test -v -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

clean:
	go clean
	rm -f $(BINARY_NAME)
	rm -f coverage.out coverage.html

image-build:
	podman build -t $(IMAGE_NAME):$(TAG) .

image-push: docker-build
	podman push $(IMAGE_NAME):$(TAG)

fmt:
	go fmt ./...

lint:
	golangci-lint run

deps:
	go mod download
	go mod tidy

.DEFAULT_GOAL := help
