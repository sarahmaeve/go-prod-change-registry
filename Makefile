VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-X main.Version=$(VERSION)"

.DEFAULT_GOAL := build

.PHONY: build clean test test-short coverage lint fmt run vet audit

build:
	go build $(LDFLAGS) -o bin/pcr-server ./cmd/server

clean:
	rm -rf bin/
	rm -f coverage.out coverage.html

test:
	go test -race -cover ./...

test-short:
	go test -short ./...

coverage:
	go test -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	open coverage.html

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

# Docker targets
.PHONY: docker-build docker-run docker-compose-up docker-compose-down

docker-build:
	docker build -t pcr-server .

docker-run: docker-build
	docker run --rm -p 8080:8080 \
		-e PCR_API_TOKENS=$${PCR_API_TOKENS:-changeme} \
		-e PCR_SESSION_SECRET=$${PCR_SESSION_SECRET:-docker-dev-secret} \
		-v pcr-data:/data \
		pcr-server

docker-compose-up:
	docker compose up --build

docker-compose-down:
	docker compose down
