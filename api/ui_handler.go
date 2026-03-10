package api

import (
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/vultisig/notification/models"
	"github.com/vultisig/notification/ui"
)

func (s *Server) registerUIRoutes(e *echo.Echo) {
	e.GET("/ui", s.uiIndex)

	staticFS, _ := fs.Sub(ui.Static, ".")
	e.GET("/ui/static/*", echo.WrapHandler(http.StripPrefix("/ui/static/", http.FileServer(http.FS(staticFS)))))

	e.GET("/ui/api/queue", s.uiQueueInfo)
	e.GET("/ui/api/vaults", s.uiListVaults)
	e.GET("/ui/api/vaults/:vault_id/devices", s.uiListDevices)
	e.GET("/ui/api/queue/stream", s.uiQueueStream)
}

func (s *Server) uiIndex(c echo.Context) error {
	tmpl, err := template.ParseFS(ui.Templates, "templates/index.html")
	if err != nil {
		s.logger.Errorf("Failed to parse UI template: %v", err)
		return c.String(http.StatusInternalServerError, "template error")
	}
	c.Response().Header().Set("Content-Type", "text/html; charset=utf-8")
	c.Response().WriteHeader(http.StatusOK)
	return tmpl.Execute(c.Response().Writer, nil)
}

func (s *Server) uiQueueInfo(c echo.Context) error {
	info, err := s.inspector.GetQueueInfo(models.QUEUE_NAME)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]int{
		"pending":   info.Pending,
		"active":    info.Active,
		"completed": info.Completed,
		"retry":     info.Retry,
		"archived":  info.Archived,
		"failed":    info.Failed,
	})
}

func (s *Server) uiListVaults(c echo.Context) error {
	vaults, err := s.db.ListAllVaults(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, vaults)
}

type uiDeviceResponse struct {
	PartyName  string    `json:"party_name"`
	DeviceType string    `json:"device_type"`
	CreatedAt  time.Time `json:"created_at"`
}

func (s *Server) uiListDevices(c echo.Context) error {
	vaultId := c.Param("vault_id")
	if vaultId == "" {
		return c.NoContent(http.StatusBadRequest)
	}
	devices, err := s.db.ListDevicesByVault(c.Request().Context(), vaultId)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	resp := make([]uiDeviceResponse, len(devices))
	for i, d := range devices {
		resp[i] = uiDeviceResponse{
			PartyName:  d.PartyName,
			DeviceType: d.DeviceType,
			CreatedAt:  d.CreatedAt,
		}
	}
	return c.JSON(http.StatusOK, resp)
}

func (s *Server) uiQueueStream(c echo.Context) error {
	c.Response().Header().Set("Content-Type", "text/event-stream")
	c.Response().Header().Set("Cache-Control", "no-cache")
	c.Response().Header().Set("Connection", "keep-alive")
	c.Response().WriteHeader(http.StatusOK)
	c.Response().Flush()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	// Send initial state immediately.
	if err := s.sendQueueEvent(c); err != nil {
		return nil
	}

	for {
		select {
		case <-c.Request().Context().Done():
			return nil
		case <-ticker.C:
			if err := s.sendQueueEvent(c); err != nil {
				return nil
			}
		}
	}
}

func (s *Server) sendQueueEvent(c echo.Context) error {
	info, err := s.inspector.GetQueueInfo(models.QUEUE_NAME)
	if err != nil {
		return err
	}
	data := fmt.Sprintf(`{"pending":%d,"active":%d,"completed":%d,"retry":%d,"archived":%d,"failed":%d}`,
		info.Pending, info.Active, info.Completed, info.Retry, info.Archived, info.Failed)
	_, err = fmt.Fprintf(c.Response().Writer, "data: %s\n\n", data)
	if err != nil {
		return err
	}
	c.Response().Flush()
	return nil
}
