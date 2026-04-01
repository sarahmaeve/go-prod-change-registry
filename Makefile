VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-X main.Version=$(VERSION)"

.DEFAULT_GOAL := build

.PHONY: build clean test test-short lint fmt run vet audit

build:
	go build $(LDFLAGS) -o bin/pcr-server ./cmd/server

clean:
	rm -rf bin/

test:
	go test -race -cover ./...

test-short:
	go test -short ./...

lint:
	golangci-lint run ./...

fmt:
	gofmt -w .
	goimports -w .

run:
	go run ./cmd/server

vet:
	go vet ./...

audit:
	go vet ./...
	govulncheck ./...
