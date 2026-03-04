package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/DataDog/datadog-go/statsd"
	"github.com/hibiken/asynq"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/labstack/gommon/log"
	"github.com/sirupsen/logrus"
	"github.com/vultisig/notification/cache"
	"github.com/vultisig/notification/models"
	"github.com/vultisig/notification/storage"
	"github.com/vultisig/notification/stream"
	"github.com/vultisig/notification/ws"
)

type Server struct {
	port           int64
	sdClient       *statsd.Client
	logger         *logrus.Logger
	db             *storage.Database
	queueClient    *asynq.Client
	cacheClient    *cache.RedisStorage
	streamStore    *stream.Store
	vapidPublicKey string
	wsHandler      *ws.Handler
}

func NewServer(port int64, sdClient *statsd.Client,
	db *storage.Database,
	queueClient *asynq.Client,
	cacheClient *cache.RedisStorage,
	streamStore *stream.Store,
	wsHandler *ws.Handler,
	vapidPublicKey string) (*Server, error) {
	if port <= 0 {
		return nil, fmt.Errorf("invalid port number: %d", port)
	}
	if sdClient == nil {
		return nil, fmt.Errorf("statsd client is nil")
	}
	if db == nil {
		return nil, fmt.Errorf("database is nil")
	}
	if queueClient == nil {
		return nil, fmt.Errorf("asynq client is nil")
	}
	if cacheClient == nil {
		return nil, fmt.Errorf("cache client is nil")
	}
	if streamStore == nil {
		return nil, fmt.Errorf("stream store is nil")
	}
	return &Server{
		port:           port,
		sdClient:       sdClient,
		logger:         logrus.WithField("module", "api").Logger,
		db:             db,
		queueClient:    queueClient,
		cacheClient:    cacheClient,
		streamStore:    streamStore,
		vapidPublicKey: vapidPublicKey,
		wsHandler:      wsHandler,
	}, nil
}

func (s *Server) StartServer() error {
	e := echo.New()
	e.Logger.SetLevel(log.DEBUG)
	e.Pre(middleware.RemoveTrailingSlash())
	e.Use(middleware.Logger())
	e.Use(middleware.Recover())
	e.Use(middleware.BodyLimit("2M"))
	e.Use(s.statsdMiddleware)
	e.Use(middleware.CORS())
	limiterStore := middleware.NewRateLimiterMemoryStoreWithConfig(
		middleware.RateLimiterMemoryStoreConfig{Rate: 5, Burst: 30, ExpiresIn: 5 * time.Minute},
	)
	e.Use(middleware.RateLimiter(limiterStore))
	e.GET("/healthz", s.Ping)
	e.GET("/ping", s.Ping)
	e.POST("/register", s.Register)
	e.DELETE("/unregister", s.Unregister)
	e.GET("/vault/:vault_id", s.IsVaultRegistered)
	e.POST("/notify", s.SendNotification)
	e.GET("/vapid-public-key", s.GetVAPIDPublicKey)
	e.GET("/ws", echo.WrapHandler(s.wsHandler))

	return e.Start(fmt.Sprintf(":%d", s.port))
}

func (s *Server) statsdMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		start := time.Now()
		err := next(c)
		duration := time.Since(start).Milliseconds()

		_ = s.sdClient.Incr("http.requests", []string{"path:" + c.Path()}, 1)
		_ = s.sdClient.Timing("http.response_time", time.Duration(duration)*time.Millisecond, []string{"path:" + c.Path()}, 1)
		_ = s.sdClient.Incr("http.status."+fmt.Sprint(c.Response().Status), []string{"path:" + c.Path(), "method:" + c.Request().Method}, 1)

		return err
	}
}

func (s *Server) Ping(c echo.Context) error {
	return c.String(http.StatusOK, "Vultisig notification server is running")
}

func (s *Server) GetVAPIDPublicKey(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]string{"public_key": s.vapidPublicKey})
}

// Register handles device registration for push notifications.
func (s *Server) Register(c echo.Context) error {
	var deviceReq models.Device
	if err := c.Bind(&deviceReq); err != nil {
		c.Logger().Errorf("Failed to bind device register request: %v", err)
		return c.NoContent(http.StatusBadRequest)
	}
	if err := deviceReq.IsValid(); err != nil {
		return c.NoContent(http.StatusBadRequest)
	}

	if err := s.db.RegisterDevice(c.Request().Context(), deviceReq); err != nil {
		c.Logger().Errorf("Failed to register device: %v", err)
		return c.NoContent(http.StatusInternalServerError)
	}
	return c.NoContent(http.StatusOK)
}

// Unregister handles device unregistration for push notifications.
// If token is provided, only the specific device is removed.
// If token is omitted, all devices for the party are removed.
func (s *Server) Unregister(c echo.Context) error {
	var req struct {
		VaultId   string `json:"vault_id"`
		PartyName string `json:"party_name"`
		Token     string `json:"token"`
	}
	if err := c.Bind(&req); err != nil {
		return c.NoContent(http.StatusBadRequest)
	}

	if req.VaultId == "" || req.PartyName == "" {
		return c.NoContent(http.StatusBadRequest)
	}

	var err error
	if req.Token != "" {
		err = s.db.UnregisterDeviceByPartyAndToken(c.Request().Context(), req.VaultId, req.PartyName, req.Token)
	} else {
		err = s.db.UnregisterDeviceByParty(c.Request().Context(), req.VaultId, req.PartyName)
	}
	if err != nil {
		c.Logger().Errorf("Failed to unregister device: %v", err)
		return c.NoContent(http.StatusInternalServerError)
	}
	return c.NoContent(http.StatusOK)
}

// IsVaultRegistered checks if a vault has any registered devices (public, no auth).
func (s *Server) IsVaultRegistered(c echo.Context) error {
	vaultId := c.Param("vault_id")
	if len(vaultId) == 0 {
		return c.NoContent(http.StatusBadRequest)
	}

	registered, err := s.db.IsDeviceRegistered(c.Request().Context(), vaultId)
	if err != nil {
		c.Logger().Errorf("Failed to check if vault is registered: %v", err)
		return c.NoContent(http.StatusInternalServerError)
	}

	if registered {
		return c.NoContent(http.StatusOK)
	}
	return c.NoContent(http.StatusNotFound)
}

// SendNotification queues a push notification and publishes to the real-time stream.
func (s *Server) SendNotification(c echo.Context) error {
	var req models.NotificationRequest
	if err := c.Bind(&req); err != nil {
		c.Logger().Errorf("Failed to bind notification request: %v", err)
		return c.NoContent(http.StatusBadRequest)
	}
	if req.VaultId == "" || req.VaultName == "" || req.LocalPartyId == "" || req.QRCodeData == "" {
		return c.NoContent(http.StatusBadRequest)
	}

	ctx := c.Request().Context()

	buf, err := json.Marshal(req)
	if err != nil {
		c.Logger().Errorf("Failed to marshal notification request: %v", err)
		return c.NoContent(http.StatusInternalServerError)
	}

	// Dedup: skip if a recent notification for this vault is still being processed.
	result, err := s.cacheClient.Get(ctx, req.VaultId)
	if err == nil && result != "" {
		return c.NoContent(http.StatusOK)
	}
	if err := s.cacheClient.Set(ctx, req.VaultId, req.VaultId, time.Second*30); err != nil {
		s.logger.Errorf("Failed to set cache for vault %s: %v", req.VaultId, err)
	}

	// Enqueue for push delivery (APNs/FCM/WebPush).
	if _, err := s.queueClient.Enqueue(asynq.NewTask(models.TypeNotification, buf),
		asynq.MaxRetry(-1),
		asynq.Timeout(time.Minute),
		asynq.Retention(time.Minute),
		asynq.Queue(models.QUEUE_NAME)); err != nil {
		s.logger.Errorf("Failed to enqueue notification task: %v", err)
		return c.NoContent(http.StatusInternalServerError)
	}

	// Publish to Redis Stream for WebSocket delivery (non-fatal if it fails).
	if err := s.streamStore.Publish(ctx, req.VaultId, stream.PublishRequest{
		VaultName:  req.VaultName,
		QRCodeData: req.QRCodeData,
	}); err != nil {
		s.logger.Errorf("Failed to publish to stream: %v", err)
	}

	return c.NoContent(http.StatusOK)
}
