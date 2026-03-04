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
	"github.com/vultisig/notification/storage"
	"github.com/vultisig/notification/stream"
)

const defaultConnLimit = 10

type Handler struct {
	stream    *stream.Store
	db        *storage.Database
	rdb       *redis.Client
	connLimit int
	logger    *logrus.Entry
}

func NewHandler(s *stream.Store, db *storage.Database, rdb *redis.Client) *Handler {
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

	conn, err := websocket.Accept(w, r, nil)
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
	for msg := range ch {
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
