# Vultisig Notification Service

A push notification server for the [Vultisig](https://vultisig.com) cryptocurrency wallet. It handles device registration, delivers push notifications to iOS, Android, and web devices, and provides real-time WebSocket delivery backed by Redis Streams.

## Architecture

The service is split into two binaries:

- **API Server** (`cmd/server`) — RESTful HTTP API for device registration, notification triggering, and WebSocket connections
- **Worker** (`cmd/worker`) — Async task processor that handles push notification delivery (APNs, FCM, Web Push)

Requests are decoupled via a Redis-backed [Asynq](https://github.com/hibiken/asynq) queue, so the API responds immediately while the worker delivers push notifications in the background. WebSocket clients receive notifications in real-time via Redis Streams with exactly-once delivery guarantees.

```text
Client → POST /register → generates auth_token → MySQL DB
                                                → returns auth_token to client

Client → POST /notify → Redis Queue → Worker → APNs / FCM / Web Push
       [auth required]  → Redis Stream ──→ WebSocket clients (real-time)

Client → GET /ws?auth_token=... → Redis Stream consumer → messages over WebSocket
                                                        ← ACK from client
```

## Prerequisites

- Go 1.25+
- MySQL
- Redis
- Apple APNs P12 certificate (for iOS notifications)

## Getting Started

### 1. Start dependencies

```bash
docker-compose up -d
```

This starts MySQL (port `3301`), Redis (port `6372`), and [Asynqmon](http://localhost:8022) for queue monitoring.

### 2. Configure

Copy and edit the config file:

```bash
cp config.example.json config.json
```

```json
{
  "server": {
    "host": "localhost",
    "port": 8080
  },
  "database": {
    "host": "localhost",
    "port": 3301,
    "user": "root",
    "password": "password",
    "database": "notification"
  },
  "redis": {
    "host": "localhost",
    "port": "6372",
    "user": "",
    "password": "",
    "db": 0
  },
  "stream": {
    "message-ttl": 60
  },
  "certificate": "./sandbox.p12",
  "certificate-password": "<p12-password>",
  "production": false
}
```

| Field | Description |
| --- | --- |
| `certificate` | Path to Apple APNs P12 certificate. Use `sandbox.p12` for development, `production.p12` for production. |
| `certificate-password` | Password for the P12 certificate. |
| `production` | `true` to use APNs production endpoint, `false` for sandbox. |
| `stream.message-ttl` | How long (seconds) messages persist in Redis Streams for disconnected WebSocket clients. Default: `60`. |

### 3. Run the API server

```bash
go run ./cmd/server
```

### 4. Run the worker

```bash
go run ./cmd/worker
```

## Authentication

All endpoints except `/ping`, `/register`, `/vapid-public-key`, and `/vault/:vault_id` require authentication via a Bearer token.

When a device registers via `POST /register`, the server generates a cryptographically random auth token, stores a SHA-256 hash in the database, and returns the raw token. All subsequent API calls must include this token:

```
Authorization: Bearer <auth_token>
```

Re-registering with an existing auth token rotates it — a new token is returned and the old one is immediately invalidated.

## API Endpoints

All endpoints are rate-limited to **5 req/s** (burst of 30) with a **2 MB** body size limit.

### `GET /ping`

Health check. No auth required.

**Response:** `200 OK` — `Vultisig notification server is running`

---

### `POST /register`

Register a device to receive notifications for a vault. No auth required for new registrations. Include `Authorization: Bearer <token>` to re-register an existing device (updates fields and rotates the auth token).

**Request body:**

```json
{
  "vault_id":    "string (required)",
  "party_name":  "string (required)",
  "token":       "string (required)",
  "device_type": "apple | android | web (required)"
}
```

**Response:** `200 OK`

```json
{
  "auth_token": "base64url-encoded-token"
}
```

Store this token — it's required for all authenticated endpoints and WebSocket connections.

---

### `DELETE /unregister`

Unregister the device identified by the auth token. **Auth required.**

**Response:** `200 OK` on success, `401` if unauthorized.

---

### `GET /vault/:vault_id`

Check whether any devices are registered for a vault. No auth required.

**Response:** `200 OK` if registered, `404 Not Found` if not.

---

### `POST /notify`

Send a notification to all devices registered for the caller's vault (except the caller). **Auth required.** The `vault_id` and sender identity are derived from the auth token.

**Request body:**

```json
{
  "vault_name":   "string (required)",
  "qr_code_data": "string (required)"
}
```

**Response:** `200 OK` — notification queued and published to stream.

---

### `GET /vapid-public-key`

Retrieve the VAPID public key for Web Push subscriptions. No auth required.

**Response:** `200 OK` — `{ "public_key": "..." }`

---

### `GET /ws?auth_token=<token>`

Upgrade to a WebSocket connection for real-time notification delivery. Auth is via query parameter (WebSocket upgrade requests don't reliably support custom headers).

**Connection limit:** Max 10 concurrent WebSocket connections per vault.

**Incoming messages (server → client):**

```json
{
  "type": "notification",
  "id": "1234567890123-0",
  "vault_name": "My Vault",
  "qr_code_data": "..."
}
```

**Outgoing messages (client → server):**

```json
{
  "type": "ack",
  "id": "1234567890123-0"
}
```

Send an ACK for each received notification. Unacknowledged messages within the TTL window will be re-delivered on reconnect.

## Notification Flow

1. `POST /notify` validates the request and sets a **30-second Redis lock** to prevent duplicate deliveries.
2. The notification task is enqueued to Redis with a 1-minute timeout.
3. The notification is also published to a Redis Stream for real-time WebSocket delivery.
4. The worker dequeues the task, looks up all registered devices for the `vault_id`, and excludes the sender's `party_name`.
5. Push notifications are dispatched by device type:
   - **Apple** — sent via APNs using the configured P12 certificate.
   - **Android** — sent via FCM (Firebase Cloud Messaging).
   - **Web** — sent via Web Push with VAPID.
6. Invalid push tokens (HTTP `410`/`400` responses) are automatically unregistered from the database.
7. WebSocket clients connected for the vault receive the notification in real-time. Disconnected clients receive pending messages on reconnect (within the message TTL window).

## Running Tests

```bash
go test ./...
```

## Queue Monitoring

When running via Docker Compose, [Asynqmon](https://github.com/hibiken/asynqmon) is available at [http://localhost:8022](http://localhost:8022) for inspecting queued, active, and failed tasks.

## Metrics

The service reports StatsD metrics to `127.0.0.1:8125` (Datadog-compatible), including request counts, response times, status codes, and notification delivery failures.
