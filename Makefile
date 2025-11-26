.PHONY: all deps build client clean test

all: build

deps:
	go mod download
	go mod tidy

build: client

client:
	go build -o bin/uplink-service ./cmd/uplink-service

client-linux-amd64:
	GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o bin/uplink-service-amd64 ./cmd/uplink-service

client-linux-arm:
	GOOS=linux GOARCH=arm GOARM=7 go build -ldflags="-s -w" -o bin/uplink-service-arm ./cmd/uplink-service

clean:
	rm -rf bin/

test:
	go test -v ./...

run:
	./bin/uplink-service -config configs/uplink.example.yml
