# WebSocket + Redis Streams: Notification Server Design

This document describes the design for adding real-time WebSocket delivery to the Vultisig notification server.

## Problem

The notification server currently delivers messages exclusively via platform push services (APNs, FCM, Web Push). This creates one gap:

**Node.js / server-side SDK** — The Vultisig SDK needs to receive signing session notifications in environments where push services aren't available (Node.js backends, CLI tools, Electron apps without push registration).

Multi-device support (multiple processes sharing the same vault+party) was addressed separately in PR #11 via a 3-column unique key `(vault_id, party_name, token)`.

## Solution

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
  ◄─── 200 OK           │                                  │
                        │              ┌─────────────────┐ │
POST /notify ──────────►│              │   server.go      │ │
                        │              │   handlers       │ │
                        │              └────────┬────────┘ │
                        │                       │           │
                        │              ┌────────┴────────┐  │
                        │              ▼                 ▼   │
                        │     ┌──────────────┐  ┌────────┐  │
                        │     │ stream.Store  │  │ Asynq  │  │
                        │     │  (Publish)    │  │ queue  │  │
                        │     └──────┬───────┘  └───┬────┘  │
                        └────────────┼──────────────┼───────┘
                                     │              │
                              Redis Streams    Redis Queue
                                     │              │
                        ┌────────────┼──────────────┼───────┐
GET /ws ───────────────►│            ▼              ▼        │
  [?vault_id=&          │  ┌──────────────┐  ┌──────────┐   │
   party_name=&token=]  │  │ stream.Store  │  │  Worker  │   │
                        │  │ (Subscribe)   │  │          │   │
   ◄─── notifications   │  │    <-chan     │  │ APNs/FCM │   │
   ───► ACK             │  └──────────────┘  │ Web Push │   │
                        │                    └──────────┘   │
                        │         ws/handler.go              │
                        └────────────────────────────────────┘
```

### Package design

**`stream/`** — Owns all Redis Stream concerns. Exposes `Publish()`, `Subscribe()` (returns `<-chan Message`), and `Ack()`. No stream commands exist outside this package. Takes its own `*redis.Client` — no shared state with the cache package.

**`ws/`** — WebSocket handler. A single `http.Handler` that blocks for the connection lifetime. No Hub, no Client struct, no goroutine pool. The `coder/websocket` library handles ping/pong automatically and supports concurrent writes without a mutex. Context cancellation propagates cleanly to all connections for graceful shutdown.

**Why `coder/websocket` over `gorilla/websocket`?**

| | gorilla/websocket | coder/websocket |
|---|---|---|
| Maintenance | Archived (read-only) | Active |
| Concurrent writes | Requires external mutex | Safe by default |
| Ping/pong | Manual goroutine | Automatic |
| Context support | Bolted on | Native |
| Close handling | Manual | Automatic on ctx cancel |

### WebSocket authentication

The WebSocket endpoint authenticates using the device's existing push token — the same credential already in the `devices` table:

```
GET /ws?vault_id=<vault_id>&party_name=<party_name>&token=<push_token>
```

The server verifies the device exists: `WHERE vault_id=? AND party_name=? AND token=?`. This is equivalent to the implicit auth of push delivery — only the device that registered with that push token can connect. No new credential storage needed.

Consumer name is derived deterministically: first 8 bytes of `sha256(vault_id + ":" + party_name + ":" + token)`, formatted as 16 hex characters (`h256[:8]`). This ensures the same device reconnects to the same consumer group position, enabling pending message re-delivery.

### Per-vault connection limit

Max 10 WebSocket connections per `vault_id`, enforced via a Redis counter (`INCR`/`DECR`). The counter has a 10-minute TTL as a safety net in case the server crashes without decrementing.

This prevents connection flooding while being generous enough for real-world usage (a vault typically has 2-3 parties, each potentially with multiple instances).

## API Changes

| Endpoint | Before | After |
|---|---|---|
| `POST /notify` | Enqueues push only | Also publishes to Redis Stream for WebSocket delivery |
| `GET /ws` | N/A | **New.** `?vault_id=&party_name=&token=`. Real-time WebSocket delivery. |

All other endpoints are unchanged.

## Client Integration

### Registration (unchanged)

```
POST /register
Content-Type: application/json

{ "vault_id": "...", "party_name": "...", "token": "...", "device_type": "web" }

→ 200 OK
```

### Sending notifications (unchanged)

```
POST /notify
Content-Type: application/json

{ "vault_id": "...", "vault_name": "My Vault", "local_party_id": "...", "qr_code_data": "..." }
```

### Receiving via WebSocket

```javascript
const ws = new WebSocket(
  'wss://api.vultisig.com/ws?vault_id=<vault_id>&party_name=<party>&token=<push_token>'
);

ws.onmessage = (event) => {
  const msg = JSON.parse(event.data);
  if (msg.type === 'notification') {
    handleSigningRequest(msg.vault_name, msg.qr_code_data);
    // ACK to prevent re-delivery
    ws.send(JSON.stringify({ type: 'ack', id: msg.id }));
  }
};
```

## Redis Keys

| Key pattern | Type | Purpose |
|---|---|---|
| `notifications:{vault_id}` | Stream | Message persistence per vault |
| `ws:{vault_id}` | Consumer group | Tracks delivery position per consumer |
| `ws:conns:{vault_id}` | String (counter) | WebSocket connection limit |
| `{vault_id}` | String (existing) | 30-second dedup lock |

## Configuration

One new config key:

```yaml
stream:
  message-ttl: 60  # seconds, default 60
```

No database schema changes. The `devices` table is unchanged from PR #11.
