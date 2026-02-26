package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
)

const (
	wsWriteWait      = 10 * time.Second
	wsPongWait       = 60 * time.Second
	wsPingPeriod     = 30 * time.Second
	wsMaxMessageSize = 512
	wsSendBufferSize = 32
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

type wsMessage struct {
	Action    string `json:"action"`
	VaultId   string `json:"vault_id"`
	PartyName string `json:"party_name"`
}

func (s *Server) HandleWS(c echo.Context) error {
	conn, err := upgrader.Upgrade(c.Response(), c.Request(), nil)
	if err != nil {
		s.logger.Errorf("ws: upgrade failed: %v", err)
		return nil
	}

	wc := &wsConn{
		conn:   conn,
		send:   make(chan []byte, wsSendBufferSize),
		vaults: make(map[string]string),
	}

	go s.wsWritePump(wc)
	s.wsReadPump(wc)
	return nil
}

func (s *Server) wsReadPump(wc *wsConn) {
	defer s.wsHub.UnregisterAll(wc)
	wc.conn.SetReadLimit(wsMaxMessageSize)
	wc.conn.SetReadDeadline(time.Now().Add(wsPongWait))
	wc.conn.SetPongHandler(func(string) error {
		wc.conn.SetReadDeadline(time.Now().Add(wsPongWait))
		return nil
	})
	for {
		_, raw, err := wc.conn.ReadMessage()
		if err != nil {
			break
		}
		var msg wsMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			s.logger.Warnf("ws: invalid message: %v", err)
			continue
		}
		switch msg.Action {
		case "register":
			if msg.VaultId == "" || msg.PartyName == "" {
				continue
			}
			s.wsHub.Register(msg.VaultId, msg.PartyName, wc)
		case "unregister":
			if msg.VaultId == "" {
				continue
			}
			s.wsHub.Unregister(msg.VaultId, wc)
		default:
			s.logger.Warnf("ws: unknown action: %s", msg.Action)
		}
	}
}

func (s *Server) wsWritePump(wc *wsConn) {
	ticker := time.NewTicker(wsPingPeriod)
	defer func() {
		ticker.Stop()
		s.wsHub.UnregisterAll(wc)
	}()
	for {
		select {
		case msg, ok := <-wc.send:
			wc.conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
			if !ok {
				wc.conn.WriteMessage(websocket.CloseMessage, nil)
				return
			}
			if err := wc.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			wc.conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
			if err := wc.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
