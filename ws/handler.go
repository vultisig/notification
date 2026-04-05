package ws

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"
	"github.com/vultisig/notification/models"
	"github.com/vultisig/notification/stream"
)

const (
	defaultConnLimit = 10
	pingInterval     = 30 * time.Second
)

// deviceFinder is satisfied by *storage.Database (and test mocks).
type deviceFinder interface {
	FindDeviceByToken(ctx context.Context, vaultID, party, token string) (*models.DeviceDBModel, error)
}

type Handler struct {
	stream    *stream.Store
	db        deviceFinder
	rdb       *redis.Client
	connLimit int
	logger    *logrus.Entry
}

// NewHandler creates a Handler. db must implement deviceFinder; *storage.Database satisfies this.
func NewHandler(s *stream.Store, db deviceFinder, rdb *redis.Client) *Handler {
	return &Handler{
		stream:    s,
		db:        db,
		rdb:       rdb,
		connLimit: defaultConnLimit,
		logger:    logrus.WithField("module", "ws"),
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	vaultID := r.URL.Query().Get("vault_id")
	party := r.URL.Query().Get("party_name")
	token := r.URL.Query().Get("token")
	if vaultID == "" || party == "" || token == "" {
		http.Error(w, "missing query params", http.StatusBadRequest)
		return
	}

	if _, err := h.db.FindDeviceByToken(r.Context(), vaultID, party, token); err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	connKey := fmt.Sprintf("ws:conns:%s", vaultID)

	// Enforce per-vault connection limit.
	count, err := h.rdb.Incr(r.Context(), connKey).Result()
	if err != nil {
		h.logger.WithError(err).Error("redis incr conn count")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if count > int64(h.connLimit) {
		h.rdb.Decr(r.Context(), connKey)
		http.Error(w, "too many connections for vault", http.StatusTooManyRequests)
		return
	}
	// Set a TTL on the counter so it doesn't leak if the server crashes.
	h.rdb.Expire(r.Context(), connKey, 10*time.Minute)

	// Cloudflare may proxy WebSocket upgrades as HTTP/1.0; the coder/websocket
	// library requires HTTP/1.1+. Force the protocol version so Accept succeeds.
	if r.ProtoMajor == 1 && r.ProtoMinor == 0 {
		r.Proto = "HTTP/1.1"
		r.ProtoMinor = 1
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		h.rdb.Decr(r.Context(), connKey)
		h.logger.WithError(err).Error("websocket accept")
		return
	}
	defer func() {
		conn.CloseNow()
		h.rdb.Decr(context.Background(), connKey)
	}()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Consumer name: deterministic per device, derived from push token identity.
	h256 := sha256.Sum256([]byte(vaultID + ":" + party + ":" + token))
	consumerName := fmt.Sprintf("%x", h256[:8]) // 16 hex chars, unique per device

	ch, err := h.stream.Subscribe(ctx, vaultID, consumerName)
	if err != nil {
		h.logger.WithError(err).Error("stream subscribe")
		conn.Close(websocket.StatusInternalError, "subscribe failed")
		return
	}

	// Read loop: reads ACK messages from the client.
	go h.readLoop(ctx, cancel, conn, vaultID)

	// Write loop: forward stream messages to the WebSocket.
	// Ping every 30s to keep the connection alive through proxies and load
	// balancers (e.g. Traefik default idle timeout is 180s).
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return
			}
			notification := models.WSNotification{
				Type:       "notification",
				ID:         msg.ID,
				VaultName:  msg.VaultName,
				QRCodeData: msg.QRCodeData,
			}
			if err := wsjson.Write(ctx, conn, notification); err != nil {
				h.logger.WithError(err).Debug("websocket write")
				return
			}
		case <-ticker.C:
			if err := conn.Ping(ctx); err != nil {
				h.logger.WithError(err).Debug("websocket ping")
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

func (h *Handler) readLoop(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn, vaultID string) {
	defer cancel()
	for {
		var ack models.WSAck
		if err := wsjson.Read(ctx, conn, &ack); err != nil {
			return // connection closed or error
		}
		if ack.Type == "ack" && ack.ID != "" {
			if err := h.stream.Ack(ctx, vaultID, ack.ID); err != nil {
				h.logger.WithError(err).WithField("msg_id", ack.ID).Error("stream ack")
			}
		}
	}
}
