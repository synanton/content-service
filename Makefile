MODULE := github.com/synanton/content-service
GO := go
GOLANGCI := golangci-lint
VERSION := $(shell cat VERSION 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test test-integration test-conformance vet lint tools clean bench docker-up docker-down migrate certs proto

build:
	$(GO) build -ldflags "$(LDFLAGS)" ./...

test:
	$(GO) test -race ./...

export TESTCONTAINERS_RYUK_CONTAINER_IMAGE ?= testcontainers/ryuk:0.12.0

test-integration:
	TESTCONTAINERS_RYUK_CONTAINER_IMAGE="$(TESTCONTAINERS_RYUK_CONTAINER_IMAGE)" \
	$(GO) test -race -tags=integration ./test/integration/... -v

test-conformance:
	$(GO) test -race ./tests/conformance/...

vet:
	$(GO) vet ./...

lint:
	$(GOLANGCI) run ./...

bench:
	$(GO) test -run=NONE -bench=. -benchmem ./pkg/digest/... ./pkg/reference/...

# Integration benchmarks against real Postgres + MinIO. Bounded iteration
# counts; record results in docs/benchmarks.md with hardware provenance.
# Never gate PRs on absolute numbers — shared runners are too noisy.
# Override the sample count for precision runs, e.g.:
#   make bench-integration BENCHTIME=1000x
BENCHTIME ?= 100x
bench-integration:
	$(GO) test -run=NONE -tags=integration -bench=. -benchmem -benchtime=$(BENCHTIME) ./test/integration/...

# Policy benchmarks only (BB-4): deadline overhead, retry distributions,
# slow-backend contrast, breaker transitions. Shorter sample count keeps
# the slow arms bounded (~1 min); use BENCHTIME=100x for doc numbers.
bench-policy:
	$(GO) test -run=NONE -tags=integration -bench='Policy|Breaker' -benchmem -benchtime=10x ./test/integration/...

tools:
	$(GO) install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

migrate:
	migrate -path migrations -database "postgres://content:content@localhost:5432/content?sslmode=disable" up

# Regenerate the gRPC stubs from proto/content/v1. Requires buf +
# protoc-gen-go + protoc-gen-go-grpc on PATH (see CONTRIBUTING).
# Run ONLY when the .proto contract changes — a diff here without an
# accompanying contract change is a regression, not an update.
proto:
	PATH="$$HOME/go/bin:$$PATH" buf lint && PATH="$$HOME/go/bin:$$PATH" buf generate

# Dev-only self-signed certificates for local mTLS smoke runs.
# Output stays in ./certs/ (gitignored). Never use in production;
# production mounts real certificates from the secret store.
certs:
	mkdir -p certs
	openssl req -x509 -newkey rsa:2048 -nodes \
		-keyout certs/service.key -out certs/service.crt \
		-days 30 -subj "/CN=content-service-dev"
	chmod 600 certs/service.key
	ls -l certs/

docker-up: certs
	docker compose up -d --build

docker-down:
	docker compose down

clean:
	rm -rf bin/
