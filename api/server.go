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
)

type Server struct {
	port           int64
	sdClient       *statsd.Client
	logger         *logrus.Logger
	db             *storage.Database
	queueClient    *asynq.Client
	cacheClient    *cache.RedisStorage
	vapidPublicKey string
}

func NewServer(port int64, sdClient *statsd.Client,
	db *storage.Database,
	queueClient *asynq.Client,
	cacheClient *cache.RedisStorage,
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
	return &Server{
		port:           port,
		sdClient:       sdClient,
		logger:         logrus.WithField("module", "api").Logger,
		db:             db,
		queueClient:    queueClient,
		cacheClient:    cacheClient,
		vapidPublicKey: vapidPublicKey,
	}, nil
}

func (s *Server) StartServer() error {
	e := echo.New()
	e.Logger.SetLevel(log.DEBUG)
	e.Pre(middleware.RemoveTrailingSlash())
	e.Use(middleware.Logger())
	e.Use(middleware.Recover())
	e.Use(middleware.BodyLimit("2M")) // set maximum allowed size for a request body to 2M
	e.Use(s.statsdMiddleware)
	e.Use(middleware.CORS())
	limiterStore := middleware.NewRateLimiterMemoryStoreWithConfig(
		middleware.RateLimiterMemoryStoreConfig{Rate: 5, Burst: 30, ExpiresIn: 5 * time.Minute},
	)
	e.Use(middleware.RateLimiter(limiterStore))
	e.GET("/ping", s.Ping)
	e.POST("/register", s.Register)
	e.GET("/vault/:vault_id", s.IsVaultRegistered)
	e.POST("/notify", s.SendNotification)
	e.GET("/vapid-public-key", s.GetVAPIDPublicKey)

	return e.Start(fmt.Sprintf(":%d", s.port))
}

func (s *Server) statsdMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		start := time.Now()
		err := next(c)
		duration := time.Since(start).Milliseconds()

		// Send metrics to statsd
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

// Register  handles device registration for push notifications
// @Summary      Register a device for push notifications
// @Description  Registers a device using its token and platform (iOS/Android).
func (s *Server) Register(c echo.Context) error {
	var deviceRegisterReq models.Device
	if err := c.Bind(&deviceRegisterReq); err != nil {
		c.Logger().Errorf("Failed to bind device register request: %v", err)
		return c.NoContent(http.StatusBadRequest)
	}

	if err := deviceRegisterReq.IsValid(); err != nil {
		return c.NoContent(http.StatusBadRequest)
	}

	if err := s.db.RegisterDevice(c.Request().Context(), deviceRegisterReq); err != nil {
		c.Logger().Errorf("Failed to register device: %v", err)
		return c.NoContent(http.StatusInternalServerError)
	}

	return nil
}

// IsVaultRegistered checks if a vault is registered
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
	return nil
}

func (s *Server) SendNotification(c echo.Context) error {
	var notificationReq models.NotificationRequest
	if err := c.Bind(&notificationReq); err != nil {
		c.Logger().Errorf("Failed to bind notification request: %v", err)
		return c.NoContent(http.StatusBadRequest)
	}

	if err := notificationReq.IsValid(); err != nil {
		return c.NoContent(http.StatusBadRequest)
	}
	buf, err := json.Marshal(notificationReq)
	if err != nil {
		c.Logger().Errorf("Failed to marshal notification request: %v", err)
		return c.NoContent(http.StatusInternalServerError)
	}

	result, err := s.cacheClient.Get(c.Request().Context(), notificationReq.VaultId)
	if err == nil && result != "" {
		// previous notification is still being processed, skip this one
		return c.NoContent(http.StatusOK)
	}

	if err := s.cacheClient.Set(c.Request().Context(), notificationReq.VaultId, notificationReq.VaultId, time.Second*30); err != nil {
		s.logger.Errorf("Failed to set cache for vault %s: %v", notificationReq.VaultId, err)
	}

	if _, err := s.queueClient.Enqueue(asynq.NewTask(models.TypeNotification, buf),
		asynq.MaxRetry(-1),
		asynq.Timeout(time.Minute),
		asynq.Retention(time.Minute),
		asynq.Queue(models.QUEUE_NAME)); err != nil {
		s.logger.Errorf("Failed to enqueue notification task: %v", err)
		return c.NoContent(http.StatusInternalServerError)
	}

	return c.NoContent(http.StatusOK)
}
