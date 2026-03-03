package api

import (
	"sync"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
)

type wsConn struct {
	conn      *websocket.Conn
	send      chan []byte
	closeOnce sync.Once
	mu        sync.RWMutex
	vaults    map[string]string // vaultId -> partyName
}

func (c *wsConn) setVault(vaultId, partyName string) {
	c.mu.Lock()
	c.vaults[vaultId] = partyName
	c.mu.Unlock()
}

func (c *wsConn) removeVault(vaultId string) {
	c.mu.Lock()
	delete(c.vaults, vaultId)
	c.mu.Unlock()
}

func (c *wsConn) partyForVault(vaultId string) (string, bool) {
	c.mu.RLock()
	p, ok := c.vaults[vaultId]
	c.mu.RUnlock()
	return p, ok
}

func (c *wsConn) allVaults() map[string]string {
	c.mu.RLock()
	cp := make(map[string]string, len(c.vaults))
	for k, v := range c.vaults {
		cp[k] = v
	}
	c.mu.RUnlock()
	return cp
}

type WSHub struct {
	mu     sync.RWMutex
	vaults map[string]map[*wsConn]struct{}
	logger *logrus.Logger
}

func NewWSHub(logger *logrus.Logger) *WSHub {
	return &WSHub{
		vaults: make(map[string]map[*wsConn]struct{}),
		logger: logger,
	}
}

func (h *WSHub) Register(vaultId, partyName string, c *wsConn) {
	c.setVault(vaultId, partyName)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.vaults[vaultId] == nil {
		h.vaults[vaultId] = make(map[*wsConn]struct{})
	}
	h.vaults[vaultId][c] = struct{}{}
	h.logger.Infof("ws: registered party %s for vault %s", partyName, vaultId)
}

func (h *WSHub) Unregister(vaultId string, c *wsConn) {
	c.removeVault(vaultId)
	h.mu.Lock()
	defer h.mu.Unlock()
	conns, ok := h.vaults[vaultId]
	if !ok {
		return
	}
	if _, exists := conns[c]; !exists {
		return
	}
	delete(conns, c)
	if len(conns) == 0 {
		delete(h.vaults, vaultId)
	}
	h.logger.Infof("ws: unregistered conn from vault %s", vaultId)
}

func (h *WSHub) UnregisterAll(c *wsConn) {
	for vaultId := range c.allVaults() {
		h.Unregister(vaultId, c)
	}
	c.closeOnce.Do(func() {
		close(c.send)
		c.conn.Close()
	})
}

func (h *WSHub) Broadcast(vaultId, excludeParty string, message []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	conns, ok := h.vaults[vaultId]
	if !ok {
		return
	}
	for c := range conns {
		if party, ok := c.partyForVault(vaultId); ok && party == excludeParty {
			continue
		}
		select {
		case c.send <- message:
		default:
			h.logger.Warnf("ws: send buffer full in vault %s, dropping conn", vaultId)
			go h.UnregisterAll(c)
		}
	}
}
