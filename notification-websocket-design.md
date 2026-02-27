# WebSocket + Redis Streams: Notification Server Design

This document describes the design for adding real-time WebSocket delivery and auth-token-based authentication to the Vultisig notification server.

## Problem

The notification server currently delivers messages exclusively via platform push services (APNs, FCM, Web Push). This creates two gaps:

1. **Node.js / server-side SDK** — The Vultisig SDK needs to receive signing session notifications in environments where push services aren't available (Node.js backends, CLI tools, Electron apps without push registration).
2. **No authentication** — The existing API has no auth. `vault_id` (a public ECDSA key) and `party_name` are both public information. Anyone who knows them can register a device, send notifications, or (with the proposed WebSocket endpoint) eavesdrop on signing session data.
3. **Multi-instance support** — Users can export and import vault shares across multiple processes/devices. The old `(vault_id, party_name)` unique constraint means only one device registration per share. A second process registering with the same vault share silently overwrites the first.

## Solution

### Auth token as device identity

Each `POST /register` call generates a 32-byte cryptographically random token. The SHA-256 hash is stored as a unique index in the `devices` table, and the raw token is returned to the client. This token serves as both:

- **Authentication** — All endpoints (except `/ping`, `/register`, `/vapid-public-key`, `/vault/:vault_id`) require `Authorization: Bearer <token>`.
- **Device identity** — Replaces the old `(vault_id, party_name)` composite unique key. Each registration gets its own token regardless of vault/party overlap.

**Why this approach?**

| Alternative | Problem |
|---|---|
| Composite key `(vault_id, party_name)` | Only one registration per share. Second process overwrites first. |
| Composite key `(vault_id, party_name, instance_id)` | Requires clients to generate and persist an instance ID. Extra field, extra plumbing, same outcome. |
| Auth token as unique ID | Each registration is independent. Multi-instance comes for free. No new fields needed on the client beyond storing the token. |

**Security properties:**
- Token is 32 bytes of `crypto/rand` — 256 bits of entropy.
- Only the SHA-256 hash is stored in the database. A DB compromise doesn't leak usable tokens.
- Tokens are compared using `crypto/subtle.ConstantTimeCompare` to prevent timing attacks.
- Re-registration with an existing token rotates it — the old token is immediately invalidated.

### WebSocket delivery via Redis Streams

WebSocket connections provide real-time notification delivery with exactly-once guarantees. Redis Streams (not Pub/Sub) back the delivery channel.

**Why Redis Streams over Pub/Sub?**

| | Redis Pub/Sub | Redis Streams |
|---|---|---|
| Persistence | None — fire and forget | Messages persist until trimmed |
| Disconnection | Messages lost | Consumer picks up where it left off |
| Acknowledgment | No concept of ACK | `XACK` confirms delivery |
| Consumer groups | N/A | Built-in — each consumer tracks its own position |
| TTL/trimming | N/A | `MINID` trims expired messages atomically with write |

For a crypto wallet, losing a signing session notification means the transaction doesn't happen. Exactly-once delivery matters.

**Message lifecycle:**

```
1. POST /notify → XADD to stream (+ Asynq enqueue for push)
2. Stream message persists for 60 seconds (MINID trimming)
3. Connected WebSocket client receives message immediately
4. Client sends ACK → XACK removes from pending
5. If client disconnects and reconnects within 60s:
   - Pending (unacked) messages are re-delivered
   - Stale messages (older than TTL) are auto-ACKed and skipped
6. After 60s without ACK, message is trimmed from the stream
```

The 60-second TTL mirrors the existing delivery channels:
- Asynq task timeout: 1 minute
- Asynq task retention: 1 minute
- Web Push TTL: 60 seconds

### Architecture

```
                        ┌─────────────────────────────────┐
POST /register ────────►│         API Server               │
  [no auth]             │                                  │
  ◄─── { auth_token }  │  ┌──────────┐  ┌─────────────┐  │
                        │  │ auth.go  │  │  server.go   │  │
POST /notify ──────────►│  │middleware │─►│  handlers    │  │
  [auth required]       │  └──────────┘  └──────┬──────┘  │
                        │                       │          │
                        │              ┌────────┴────────┐ │
                        │              ▼                 ▼  │
                        │     ┌──────────────┐  ┌────────┐ │
                        │     │ stream.Store  │  │ Asynq  │ │
                        │     │  (Publish)    │  │ queue  │ │
                        │     └──────┬───────┘  └───┬────┘ │
                        └────────────┼──────────────┼──────┘
                                     │              │
                              Redis Streams    Redis Queue
                                     │              │
                        ┌────────────┼──────────────┼──────┐
GET /ws ───────────────►│            ▼              ▼      │
  [auth via query]      │  ┌──────────────┐  ┌──────────┐  │
                        │  │ stream.Store  │  │  Worker  │  │
                        │  │ (Subscribe)   │  │          │  │
   ◄─── notifications   │  │    <-chan     │  │ APNs/FCM │  │
   ───► ACK             │  └──────────────┘  │ Web Push │  │
                        │                    └──────────┘  │
                        │         ws/handler.go            │
                        └──────────────────────────────────┘
```

### Package design

The implementation follows idiomatic Go patterns with clear package boundaries:

**`stream/`** — Owns all Redis Stream concerns. Exposes `Publish()`, `Subscribe()` (returns `<-chan Message`), and `Ack()`. No stream commands exist outside this package. Takes its own `*redis.Client` — no shared state with the cache package.

**`ws/`** — WebSocket handler. A single `http.Handler` that blocks for the connection lifetime. No Hub, no Client struct, no goroutine pool. The `coder/websocket` library handles ping/pong automatically and supports concurrent writes without a mutex. Context cancellation propagates cleanly to all connections for graceful shutdown.

**`api/auth.go`** — Echo middleware for Bearer token validation. Computes SHA-256, queries DB, stores the authenticated device in the request context.

**Why `coder/websocket` over `gorilla/websocket`?**

| | gorilla/websocket | coder/websocket |
|---|---|---|
| Maintenance | Archived (read-only) | Active |
| Concurrent writes | Requires external mutex | Safe by default |
| Ping/pong | Manual goroutine | Automatic |
| Context support | Bolted on | Native |
| Close handling | Manual | Automatic on ctx cancel |

### Per-vault connection limit

Max 10 WebSocket connections per `vault_id`, enforced via a Redis counter (`INCR`/`DECR`). The counter has a 10-minute TTL as a safety net in case the server crashes without decrementing.

This prevents connection flooding while being generous enough for real-world usage (a vault typically has 2-3 parties, each potentially with multiple instances).

## API Changes

| Endpoint | Before | After |
|---|---|---|
| `POST /register` | No auth. Returns `204`. Upserts on `(vault_id, party_name)`. | No auth (new) or Bearer token (re-register). Returns `200 { auth_token }`. Creates new row per registration. |
| `DELETE /unregister/...` | Path params `/:vault_id/:party_name`. No auth. | `DELETE /unregister`. Auth token identifies device. |
| `POST /notify` | Body includes `vault_id`, `local_party_id`. No auth. | Body: `vault_name`, `qr_code_data` only. Auth required. Vault/party derived from token. Also publishes to Redis Stream. |
| `GET /vault/:vault_id` | No auth. | **Unchanged.** |
| `GET /ws` | N/A | **New.** `?auth_token=<token>`. Real-time WebSocket delivery. |

## Client Integration

### Registration

```
POST /register
Content-Type: application/json

{ "vault_id": "...", "party_name": "...", "token": "...", "device_type": "web" }

→ 200 { "auth_token": "dG9rZW4..." }
```

Store `auth_token` persistently. It's required for all subsequent calls.

### Sending notifications

```
POST /notify
Authorization: Bearer dG9rZW4...
Content-Type: application/json

{ "vault_name": "My Vault", "qr_code_data": "..." }
```

### Receiving via WebSocket

```javascript
const ws = new WebSocket('wss://api.vultisig.com/ws?auth_token=dG9rZW4...');

ws.onmessage = (event) => {
  const msg = JSON.parse(event.data);
  if (msg.type === 'notification') {
    handleSigningRequest(msg.vault_name, msg.qr_code_data);
    // ACK to prevent re-delivery
    ws.send(JSON.stringify({ type: 'ack', id: msg.id }));
  }
};
```

### Re-registration (token rotation)

```
POST /register
Authorization: Bearer dG9rZW4...
Content-Type: application/json

{ "vault_id": "...", "party_name": "...", "token": "new-push-token", "device_type": "web" }

→ 200 { "auth_token": "bmV3dG9rZW4..." }
// Old token is invalidated. Store and use the new one.
```

## Redis Keys

| Key pattern | Type | Purpose |
|---|---|---|
| `notifications:{vault_id}` | Stream | Message persistence per vault |
| `ws:{vault_id}` | Consumer group | Tracks delivery position per consumer |
| `ws:conns:{vault_id}` | String (counter) | WebSocket connection limit |
| `{vault_id}` | String (existing) | 30-second dedup lock |

## Migration Notes

The `devices` table schema changes:
- **Added column:** `auth_token_hash` (`CHAR(64)`, unique index `idx_auth_token`)
- **Removed index:** `idx_vault_party` (composite unique on `vault_id + party_name`)
- **Added index:** `idx_vault_id` (non-unique, for notification lookup queries)

GORM's `AutoMigrate` handles adding the column and new indexes. The old unique index may need to be dropped manually if AutoMigrate doesn't remove it:

```sql
ALTER TABLE devices DROP INDEX idx_vault_party;
```

Existing device rows will have an empty `auth_token_hash`. They won't be accessible via the new auth system and will need to re-register. Push delivery via the Asynq worker is unaffected — it queries by `vault_id`, not by auth token.
