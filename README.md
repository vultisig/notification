# Vultisig Notification Service

A push notification server for the [Vultisig](https://vultisig.com) cryptocurrency wallet. It handles device registration, delivers push notifications to iOS, Android, and web devices, and provides real-time WebSocket delivery backed by Redis Streams.

## Architecture

The service is split into two binaries:

- **API Server** (`cmd/server`) — RESTful HTTP API for device registration, notification triggering, and WebSocket connections
- **Worker** (`cmd/worker`) — Async task processor that handles push notification delivery (APNs, FCM, Web Push)

Requests are decoupled via a Redis-backed [Asynq](https://github.com/hibiken/asynq) queue, so the API responds immediately while the worker delivers push notifications in the background. WebSocket clients receive notifications in real-time via Redis Streams with exactly-once delivery guarantees.

```text
Client → POST /register → PostgreSQL DB (upsert on vault_id + party_name + token)

Client → POST /notify → Redis Queue → Worker → APNs / FCM / Web Push
                      → Redis Stream ──→ WebSocket clients (real-time)

Client → GET /ws?vault_id=&party_name=&token= → Redis Stream consumer → messages
                                               ← ACK from client
```

## Prerequisites

- Go 1.25+
- PostgreSQL
- Redis
- Apple APNs P12 certificate (for iOS notifications)

## Getting Started

### 1. Start dependencies

```bash
docker-compose up -d
```

This starts PostgreSQL (port `5433`), Redis (port `6372`), and [Asynqmon](http://localhost:8022) for queue monitoring.

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
    "dsn": "host=localhost port=5433 user=postgres password= dbname=notification sslmode=disable"
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

Alternatively, set `redis.uri` to a Redis connection URL (e.g. `redis://localhost:6372/0`) instead of host/port fields.

| Field | Description |
| --- | --- |
| `database.dsn` | PostgreSQL connection string. |
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

## API Endpoints

All endpoints are rate-limited to **5 req/s** (burst of 30) with a **2 MB** body size limit.

### `GET /ping`

Health check. No auth required.

**Response:** `200 OK` — `Vultisig notification server is running`

---

### `POST /register`

Register a device to receive notifications for a vault. No auth required. Re-registering with the same `(vault_id, party_name, token)` tuple updates `device_type` and is idempotent.

**Request body:**

```json
{
  "vault_id":    "string (required)",
  "party_name":  "string (required)",
  "token":       "string (required)",
  "device_type": "apple | android | web (required)"
}
```

**Response:** `200 OK` (no body)

---

### `DELETE /unregister`

Unregister one or all devices for a party.

**Request body:**

```json
{
  "vault_id":   "string (required)",
  "party_name": "string (required)",
  "token":      "string (optional)"
}
```

If `token` is provided, only that specific device is removed. If omitted, all devices for the `(vault_id, party_name)` pair are removed.

**Response:** `200 OK` on success.

---

### `GET /vault/:vault_id`

Check whether any devices are registered for a vault. No auth required.

**Response:** `200 OK` if registered, `404 Not Found` if not.

---

### `POST /notify`

Send a notification to all devices registered for the vault (except the sender). No auth required.

**Request body:**

```json
{
  "vault_id":       "string (required)",
  "vault_name":     "string (required)",
  "local_party_id": "string (required)",
  "qr_code_data":   "string (required)"
}
```

`local_party_id` identifies the sender — the server excludes that party from push recipients.

**Response:** `200 OK` — notification queued and published to stream.

---

### `GET /vapid-public-key`

Retrieve the VAPID public key for Web Push subscriptions. No auth required.

**Response:** `200 OK` — `{ "public_key": "..." }`

---

### `GET /ws?vault_id=<id>&party_name=<party>&token=<push_token>`

Upgrade to a WebSocket connection for real-time notification delivery. Auth is via the device's registered push token — the server verifies the `(vault_id, party_name, token)` combination exists in the database.

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
4. The worker dequeues the task, looks up all registered devices for the `vault_id`, and excludes the sender's `local_party_id`.
5. Push notifications are dispatched by device type:
   - **Apple** — sent via APNs using the configured P12 certificate.
   - **Android** — sent via FCM (Firebase Cloud Messaging).
   - **Web** — sent via Web Push with VAPID.
6. Invalid push tokens (HTTP `410`/`400` responses) are automatically unregistered from the database.
7. WebSocket clients connected for the vault receive the notification in real-time. Disconnected clients receive pending messages on reconnect (within the message TTL window).

## Client Integration

### Sending notifications

Any HTTP client can trigger notifications. No auth required:

```
POST /notify
Content-Type: application/json

{
  "vault_id":       "ecdsa-public-key-of-vault",
  "vault_name":     "My Vault",
  "local_party_id": "party-1",
  "qr_code_data":   "vultisig://signing/..."
}
```

The server deduplicates notifications per vault (30-second window), then:
1. Enqueues push delivery (APNs/FCM/Web Push) via Asynq
2. Publishes to Redis Stream for connected WebSocket clients
3. Excludes `local_party_id` from push recipients (sender doesn't receive its own notification)

The **Vultisig SDK** handles this via `sdk.notifications.notifyVaultMembers(options)`.

---

### Receiving notifications via WebSocket (Node.js / server-side)

For environments without platform push support (Node.js backends, CLI tools, Electron main process), use the WebSocket endpoint.

**Step 1 — Register with a stable token**

Generate a persistent random token on first run and store it alongside the vault:

```
POST /register
Content-Type: application/json

{
  "vault_id":    "<vault_id>",
  "party_name":  "<party_name>",
  "token":       "<stable-uuid-stored-locally>",
  "device_type": "web"
}
→ 200 OK
```

**Step 2 — Connect WebSocket**

```
GET wss://api.vultisig.com/ws?vault_id=<vault_id>&party_name=<party_name>&token=<token>
```

The server verifies the `(vault_id, party_name, token)` tuple exists in the database. Max 10 concurrent connections per vault.

**Step 3 — Handle messages and ACK**

```javascript
const ws = new WebSocket(
  'wss://api.vultisig.com/ws?vault_id=<vault_id>&party_name=<party>&token=<token>'
);

ws.onmessage = (event) => {
  const msg = JSON.parse(event.data);
  if (msg.type === 'notification') {
    handleSigningRequest(msg.vault_name, msg.qr_code_data);
    // ACK to prevent re-delivery on reconnect
    ws.send(JSON.stringify({ type: 'ack', id: msg.id }));
  }
};
```

Always ACK each message. Unacknowledged messages within the 60-second TTL are re-delivered on reconnect.

**Reconnection handling**

Implement exponential backoff on disconnect. On reconnect, the server automatically re-delivers any pending unacknowledged messages.

> **SDK gap note**: The Vultisig SDK's `PushNotificationService` does not yet include a WebSocket client. Node.js consumers must implement the above directly until the SDK adds `WebSocketNotificationService` support.

## Running Tests

```bash
go test -v -race ./...
```

## Queue Monitoring

When running via Docker Compose, [Asynqmon](https://github.com/hibiken/asynqmon) is available at [http://localhost:8022](http://localhost:8022) for inspecting queued, active, and failed tasks.

## Metrics

The service reports StatsD metrics to `127.0.0.1:8125` (Datadog-compatible), including request counts, response times, status codes, and notification delivery failures.

## Design Notes

### WebSocket keepalive pings

The server sends a WebSocket ping every **30 seconds** during idle periods. Without this, reverse proxies and load balancers would silently drop long-lived connections that have no traffic:

| Infrastructure | Default idle timeout |
| --- | --- |
| Traefik (our ingress) | 180s |
| AWS ALB / ELB | 60s |
| GCP GCLB | 600s |
| Nginx | 75s |

The 30s interval keeps the connection alive under all of these. The client's WebSocket library handles the pong response automatically; no application-level handling is required.

### Redis Streams over Pub/Sub

Redis Pub/Sub is fire-and-forget — messages sent while a client is disconnected are lost. Redis Streams persist messages until they are explicitly acknowledged or trimmed, giving disconnected clients a recovery window (configurable via `stream.message-ttl`, default 60s). For a crypto wallet where a missed signing session means a failed transaction, this durability guarantee matters.

### WebSocket authentication via push token

The WebSocket endpoint uses the device's existing push token (`?vault_id=&party_name=&token=`) instead of a separate credential. The server checks that the `(vault_id, party_name, token)` tuple exists in the `devices` table — the same record created at registration. This means no new secrets to store, rotate, or transmit, and no additional auth layer to maintain.

### Deterministic consumer names

Each WebSocket connection's Redis Stream consumer name is `sha256(vault_id + ":" + party_name + ":" + token)[:16]`. The same device always maps to the same consumer name, so on reconnect it resumes from its exact position in the stream — picking up any unacknowledged messages rather than starting fresh.

### Per-vault connection limit via Redis counter

The connection limit (max 10 per vault) is enforced with a Redis `INCR`/`DECR` counter rather than in-process state. This means the limit is respected across multiple server replicas. The counter carries a 10-minute TTL as a safety net: if the server crashes without decrementing, the counter self-heals rather than permanently blocking new connections.

### `coder/websocket` over `gorilla/websocket`

`gorilla/websocket` is archived (read-only). `coder/websocket` is actively maintained, has native context support for clean shutdown propagation, and handles concurrent writes safely by default — no external mutex required.
