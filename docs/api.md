# Content Service API

Extracted from `internal/adapter/http/handler.go` and `internal/adapter/grpc/server.go`. The REST and gRPC surfaces implement the same contract with the same error semantics; the reference format is already frozen.

The gRPC contract is defined in `proto/content/v1/content.proto` (generated stubs in `gen/`, via `make proto`). Server reflection is enabled, so `grpcurl` works without a descriptor set.

---

## Conventions

- **Base path:** `/api/v1`.
- **Content type:** `application/json` for `POST` with body; `Accept: application/json`.
- **Tenant scope:** every request carries `tenant_id` via the `X-Tenant-ID` header (preferred), the `tenant_id` query parameter, or the JSON body field. Header wins when several are present.
- **Authentication (opt-in):** with `CONTENT_AUTH_ENABLED=true`, requests carry `Authorization: Bearer <context_token>` (gRPC: `authorization` metadata) issued by the Security Provider. The tenant is then derived from the validated context: a present-but-mismatched hint (`X-Tenant-ID`, `?tenant_id=`, body `tenant_id`, `x-tenant-id` metadata) is rejected with `403 CONFLICTING_TENANT` (gRPC: `PermissionDenied`), missing/invalid/expired tokens with `401` (gRPC: `Unauthenticated`), and a disagreeing `X-User-ID` with `403`. Without the flag, tenant scope is unauthenticated (dev only).
- **Binary fields** (`payload`): JSON base64 strings.
- **Error shape:**

```json
{
  "code": "NOT_FOUND | BAD_REQUEST | INTEGRITY_MISMATCH | INTERNAL_ERROR",
  "message": "string"
}
```

Cross-tenant reads return `404 NOT_FOUND` with the same body as a genuinely missing artifact — never a distinct code, message, or timing signal.

---

## Endpoints

### `POST /api/v1/artifacts`

**Purpose:** publish an immutable artifact. Idempotent by `(tenant_id, digest)`.

**Request:**

```json
{
  "tenant_id": "acme",
  "payload": "<base64>",
  "content_type": "application/octet-stream",
  "owner_id": "intent-123",
  "retention_days": 30,
  "serializer_version": 1
}
```

**Response:** `201 Created`:

```json
{
  "reference": "v1:artifact_id=...&digest=sha256%3A...&generation=1&serializer_version=1&tenant_id=acme",
  "artifact_id": "550e8400-e29b-41d4-a716-446655440000",
  "digest": "sha256:abc123...",
  "dedup_hit": false,
  "storage_tier": "inline"
}
```

A second `POST` with identical bytes for the same tenant returns the same `artifact_id` with `dedup_hit: true`. The same bytes for a different tenant return a different `artifact_id` with `dedup_hit: false`.

### `GET /api/v1/artifacts/{id}?tenant_id=acme`

**Purpose:** retrieve payload bytes with integrity verification.

**Response:** `200 OK`:

```json
{
  "payload": "<base64>",
  "digest": "sha256:abc123...",
  "content_type": "application/octet-stream",
  "artifact_id": "550e8400-..."
}
```

Unknown id, or an id owned by another tenant: `404 NOT_FOUND` with an indistinguishable body. Corrupt bytes: `409` with `INTEGRITY_MISMATCH`.

### `HEAD /api/v1/artifacts/{id}?tenant_id=acme`

**Purpose:** existence check without fetching bytes. Returns `200` with an `X-Artifact-ID` header, or indistinguishable `404`. Full metadata (`Head` with digest, size, retention) is a future capability.

### `GET /api/v1/capabilities`

**Purpose:** advertise conformance level and optional features.

**Response:** `200 OK`:

```json
{
  "conformance_level": "CORE",
  "features": ["tenant_isolation", "content_addressable_storage", "integrity_verification"],
  "storage_tiers": ["inline", "parquet", "s3"]
}
```

### `GET /healthz`

Liveness: `200` with body `ok` while the process serves.

### `GET /readyz`

Readiness: `200` with body `ready` when Postgres pings; `503` otherwise.

---

## gRPC

Service `content.v1.ContentService` (default `:50051`):

| RPC | Mirrors | Error mapping |
| --- | ------- | ------------- |
| `Put(PutRequest)` | `POST /api/v1/artifacts` | missing tenant/payload → `InvalidArgument` |
| `Get(GetRequest)` | `GET /api/v1/artifacts/{id}` | unknown or foreign id → `NotFound`; corrupt bytes → `DataLoss` |
| `Head(HeadRequest)` | `HEAD /api/v1/artifacts/{id}` | unknown or foreign id → `NotFound` |
| `Capabilities(CapabilitiesRequest)` | `GET /api/v1/capabilities` | — |

Tenant scope travels in request fields (`tenant_id`), matching the REST `X-Tenant-ID` contract.

```bash
grpcurl -plaintext \
  -d '{"tenant_id": "acme", "payload": "<base64>", "content_type": "text/plain"}' \
  localhost:50051 content.v1.ContentService/Put

grpcurl -plaintext \
  -d '{"tenant_id": "acme", "artifact_id": "<uuid>"}' \
  localhost:50051 content.v1.ContentService/Get
```

## curl Cookbook

```bash
BASE=http://localhost:8080

PAYLOAD=$(echo -n "hello content-service" | base64 -w0)

REF=$(curl -s -X POST $BASE/api/v1/artifacts \
  -H "Content-Type: application/json" \
  -H "X-Tenant-ID: acme" \
  -d "{\"payload\":\"$PAYLOAD\",\"content_type\":\"text/plain\",\"owner_id\":\"demo\",\"retention_days\":30}" \
  | python3 -c "import sys,json; print(json.load(sys.stdin)['artifact_id'])")

curl -s "$BASE/api/v1/artifacts/$REF?tenant_id=acme" | head -c 200
curl -s -o /dev/null -w "%{http_code}\n" "$BASE/api/v1/artifacts/$REF?tenant_id=other-tenant"
curl -s $BASE/api/v1/capabilities
curl -s $BASE/readyz
```
