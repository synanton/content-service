# Content Service — multi-stage, static binaries on distroless.
#
# Build: docker build --build-arg VERSION=$(cat VERSION) .
# Run (env-only, no config file):
#   docker run --rm -p 8080:8080 \
#     -e CONTENT_DB_URL='postgres://content:content@host:5432/content?sslmode=disable' \
#     -e CONTENT_API_KEY=secret \
#     content-service
# Jobs reuse the same image with an overridden entrypoint, e.g.:
#   docker run --rm --entrypoint /srv/disposition-job content-service
#
# Base choice (pinned by scope): distroless/static:nonroot — CA certs
# included (scratch lacks them), no shell to exploit, standard non-root
# user 65532. The API server serves docker --health-cmd via /readyz;
# jobs are ticker loops with no health endpoint by construction.

ARG GO_VERSION=1.23

FROM golang:${GO_VERSION}-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
	-ldflags "-s -w -X main.version=${VERSION}" \
	-o /out/ ./cmd/...

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out /srv
# Non-root is the image default (65532); stated explicitly so a base
# change that drops it fails review, not silently runs as root.
USER 65532:65532
EXPOSE 8080 50051
ENTRYPOINT ["/srv/content-api"]
