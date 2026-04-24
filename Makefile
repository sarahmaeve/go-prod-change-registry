VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-X main.Version=$(VERSION)"

.DEFAULT_GOAL := build

.PHONY: build clean test test-short coverage lint fmt run vet audit smoke smoke-docker

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

# Smoke / integration tests against a running pcr-server. Two flavours:
#   make smoke         spawns an ephemeral local server on :18080
#   make smoke-docker  hits whatever is on :8080 (e.g. `make docker-compose-up` first)
# Override SMOKE_TOKEN to match PCR_API_TOKENS on the target.
SMOKE_TOKEN ?= smoke-token-abc
SMOKE_DOCKER_URL ?= http://localhost:8080
SMOKE_DOCKER_TOKEN ?= changeme

smoke:
	go run ./cmd/smoke --start-local --token=$(SMOKE_TOKEN)

smoke-docker:
	go run ./cmd/smoke --base-url=$(SMOKE_DOCKER_URL) --token=$(SMOKE_DOCKER_TOKEN)

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
