.PHONY: build test test-conformance run migrate clean docker-up docker-down

build:
	go build ./...

test:
	go test ./...

test-conformance:
	go test ./tests/conformance/... -v

run:
	go run ./cmd/content-api

migrate:
	migrate -path migrations -database "postgres://content:content@localhost:5432/content?sslmode=disable" up

clean:
	rm -f content-api
	go clean ./...

docker-up:
	docker compose up -d postgres minio migrate

docker-down:
	docker compose down
