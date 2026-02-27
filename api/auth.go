package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/vultisig/notification/models"
)

const deviceContextKey = "auth_device"

func (s *Server) authMiddleware(skipper middleware.Skipper) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if skipper != nil && skipper(c) {
				return next(c)
			}

			authHeader := c.Request().Header.Get("Authorization")
			if authHeader == "" {
				return c.NoContent(http.StatusUnauthorized)
			}

			rawToken, ok := strings.CutPrefix(authHeader, "Bearer ")
			if !ok || rawToken == "" {
				return c.NoContent(http.StatusUnauthorized)
			}

			hash := hashToken(rawToken)
			device, err := s.db.FindDeviceByAuthTokenHash(c.Request().Context(), hash)
			if err != nil {
				return c.NoContent(http.StatusUnauthorized)
			}

			c.Set(deviceContextKey, device)
			return next(c)
		}
	}
}

func generateAuthToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate auth token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hashToken(rawToken string) string {
	h := sha256.Sum256([]byte(rawToken))
	return fmt.Sprintf("%x", h)
}

func deviceFromContext(c echo.Context) *models.DeviceDBModel {
	v := c.Get(deviceContextKey)
	if v == nil {
		return nil
	}
	device, ok := v.(*models.DeviceDBModel)
	if !ok {
		return nil
	}
	return device
}
