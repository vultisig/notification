package ws

import (
	"context"
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
	authToken := r.URL.Query().Get("auth_token")
	if authToken == "" {
		http.Error(w, "missing auth_token", http.StatusUnauthorized)
		return
	}

	device, err := h.lookupDevice(r.Context(), authToken)
	if err != nil {
		http.Error(w, "invalid auth_token", http.StatusUnauthorized)
		return
	}

	vaultID := device.VaultId
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

	// Use a prefix of the auth token hash as a deterministic, unique consumer name.
	consumerName := device.AuthTokenHash[:16]

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

func (h *Handler) lookupDevice(ctx context.Context, rawToken string) (*models.DeviceDBModel, error) {
	hash := hashToken(rawToken)
	return h.db.FindDeviceByAuthTokenHash(ctx, hash)
}
