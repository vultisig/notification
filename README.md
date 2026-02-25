# Vultisig Notification Service

A push notification server for the [Vultisig](https://vultisig.com) cryptocurrency wallet. It handles device registration and delivers push notifications to iOS and Android devices when a keysign request is initiated.

## Architecture

The service is split into two binaries:

- **API Server** (`cmd/server`) — RESTful HTTP API for device registration and notification triggering
- **Worker** (`cmd/worker`) — Async task processor that handles actual notification delivery

Requests are decoupled via a Redis-backed [Asynq](https://github.com/hibiken/asynq) queue, so the API responds immediately while the worker delivers notifications in the background.

```text
Client → API Server → Redis Queue → Worker → APNs / FCM
                              ↕
                          MySQL DB
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

Health check.

**Response:** `200 OK` — `Vultisig notification server is running`

---

### `POST /register`

Register a device to receive push notifications for a vault.

**Request body:**

```json
{
  "vault_id":   "string (required)",
  "party_name": "string (required)",
  "token":      "string (required)",
  "device_type": "ios | android (required)"
}
```

**Responses:** `204 No Content` on success, `400` on invalid input, `500` on server error.

---

### `DELETE /unregister/:vault_id/:party_name`

Unregister a device from push notifications for a vault.

**Path parameters:**

| Parameter | Description |
| --- | --- |
| `vault_id` | The vault identifier |
| `party_name` | The local party ID of the device to unregister |

**Responses:** `200 OK` on success, `400` if parameters are missing, `500` on server error or if no matching device is found.

---

### `GET /vault/:vault_id`

Check whether any devices are registered for a vault.

**Responses:** `200 OK` if registered, `204 No Content` if not.

---

### `POST /notify`

Trigger a push notification to all devices registered for a vault (except the requesting device).

**Request body:**

```json
{
  "vault_id":       "string (required)",
  "vault_name":     "string (required)",
  "local_party_id": "string (required)",
  "qr_code_data":   "string (required)"
}
```

**Responses:** `204 No Content` — notification has been queued. Returns immediately.

## Notification Flow

1. `POST /notify` validates the request and sets a **30-second Redis lock** to prevent duplicate deliveries.
2. The notification task is enqueued to Redis with a 1-minute timeout.
3. The worker dequeues the task, looks up all registered devices for the `vault_id`, and excludes the device identified by `local_party_id`.
4. Notifications are dispatched by device type:
   - **iOS** — sent via APNs using the configured P12 certificate.
   - **Android** — FCM integration (in progress).
5. Invalid APNs tokens (HTTP `410`/`400` responses) are automatically unregistered from the database.

## Running Tests

```bash
go test ./...
```

## Queue Monitoring

When running via Docker Compose, [Asynqmon](https://github.com/hibiken/asynqmon) is available at [http://localhost:8022](http://localhost:8022) for inspecting queued, active, and failed tasks.

## Metrics

The service reports StatsD metrics to `127.0.0.1:8125` (Datadog-compatible), including request counts, response times, status codes, and notification delivery failures.
