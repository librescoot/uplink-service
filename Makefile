BINARY_NAME := uplink-service
BUILD_DIR := bin
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-w -s -X main.version=$(VERSION)"
MAIN := ./cmd/uplink-service

.PHONY: build build-arm build-host dist clean test fmt deps lint run

build:
	mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) $(MAIN)

build-arm: build

build-host:
	mkdir -p $(BUILD_DIR)
	go build -ldflags "-X main.version=$(VERSION)" -o $(BUILD_DIR)/$(BINARY_NAME) $(MAIN)

dist: build

clean:
	rm -rf $(BUILD_DIR)

test:
	go test -v ./...

fmt:
	go fmt ./...

deps:
	go mod download && go mod tidy

lint:
	golangci-lint run

run: build-host
	./$(BUILD_DIR)/$(BINARY_NAME) -config configs/uplink.example.yml
