package ws

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"
	"github.com/vultisig/notification/models"
	"github.com/vultisig/notification/stream"
)

// mockDeviceFinder implements deviceFinder.
type mockDeviceFinder struct {
	err error
}

func (m *mockDeviceFinder) FindDeviceByToken(_ context.Context, _, _, _ string) (*models.DeviceDBModel, error) {
	if m.err != nil {
		return nil, m.err
	}
	return &models.DeviceDBModel{}, nil
}

// testEnv holds shared test infrastructure.
type testEnv struct {
	mr    *miniredis.Miniredis
	rdb   *redis.Client
	store *stream.Store
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })
	s := stream.NewStore(rdb, 60*time.Second)
	return &testEnv{mr: mr, rdb: rdb, store: s}
}

func newTestHandler(env *testEnv, finder deviceFinder, connLimit int) *Handler {
	return &Handler{
		stream:    env.store,
		db:        finder,
		rdb:       env.rdb,
		connLimit: connLimit,
		logger:    logrus.WithField("module", "ws-test"),
	}
}

// wsURL converts an httptest server URL to a ws:// URL with the given query params.
func wsURL(server *httptest.Server, params string) string {
	return strings.Replace(server.URL, "http://", "ws://", 1) + "/ws?" + params
}

func TestMissingParams(t *testing.T) {
	env := newTestEnv(t)
	h := newTestHandler(env, &mockDeviceFinder{}, defaultConnLimit)
	srv := httptest.NewServer(h)
	defer srv.Close()

	cases := []struct {
		name   string
		params string
	}{
		{"no params", ""},
		{"missing vault_id", "party_name=p&token=t"},
		{"missing party_name", "vault_id=v&token=t"},
		{"missing token", "vault_id=v&party_name=p"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := http.Get(srv.URL + "/ws?" + tc.params)
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("status = %d; want %d", resp.StatusCode, http.StatusBadRequest)
			}
		})
	}
}

func TestUnauthorized(t *testing.T) {
	env := newTestEnv(t)
	h := newTestHandler(env, &mockDeviceFinder{err: errors.New("not found")}, defaultConnLimit)
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/ws?vault_id=v&party_name=p&token=bad")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d; want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

func TestConnectionLimit(t *testing.T) {
	env := newTestEnv(t)
	h := newTestHandler(env, &mockDeviceFinder{}, 2)
	srv := httptest.NewServer(h)
	defer srv.Close()

	ctx := context.Background()

	// Open 2 connections (at limit).
	var conns []*websocket.Conn
	for i := 0; i < 2; i++ {
		url := wsURL(srv, fmt.Sprintf("vault_id=vlimit&party_name=p%d&token=t%d", i, i))
		conn, _, err := websocket.Dial(ctx, url, nil)
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		conns = append(conns, conn)
	}
	defer func() {
		for _, c := range conns {
			c.CloseNow()
		}
	}()

	// Third connection should be rejected with 429.
	url := wsURL(srv, "vault_id=vlimit&party_name=p3&token=t3")
	_, resp, err := websocket.Dial(ctx, url, nil)
	if err == nil {
		t.Fatal("expected dial to fail, but succeeded")
	}
	if resp != nil && resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("status = %d; want %d", resp.StatusCode, http.StatusTooManyRequests)
	}
}

func TestNotificationDelivery(t *testing.T) {
	env := newTestEnv(t)
	h := newTestHandler(env, &mockDeviceFinder{}, defaultConnLimit)
	srv := httptest.NewServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	url := wsURL(srv, "vault_id=vtest&party_name=party1&token=token1")
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	// Small delay so the subscribe goroutine is ready.
	time.Sleep(50 * time.Millisecond)

	if err := env.store.Publish(ctx, "vtest", stream.PublishRequest{
		VaultName:  "Test Vault",
		QRCodeData: "vultisig://signing/abc",
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	var notification models.WSNotification
	if err := wsjson.Read(ctx, conn, &notification); err != nil {
		t.Fatalf("read notification: %v", err)
	}

	if notification.Type != "notification" {
		t.Errorf("type = %q; want %q", notification.Type, "notification")
	}
	if notification.VaultName != "Test Vault" {
		t.Errorf("vault_name = %q; want %q", notification.VaultName, "Test Vault")
	}
	if notification.QRCodeData != "vultisig://signing/abc" {
		t.Errorf("qr_code_data = %q; want %q", notification.QRCodeData, "vultisig://signing/abc")
	}
	if notification.ID == "" {
		t.Error("id is empty")
	}
}

func TestAckFlow(t *testing.T) {
	env := newTestEnv(t)
	h := newTestHandler(env, &mockDeviceFinder{}, defaultConnLimit)
	srv := httptest.NewServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	url := wsURL(srv, "vault_id=vack&party_name=party1&token=token1")
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	time.Sleep(50 * time.Millisecond)
	if err := env.store.Publish(ctx, "vack", stream.PublishRequest{
		VaultName:  "Ack Vault",
		QRCodeData: "ack-test",
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	// Receive notification.
	var notification models.WSNotification
	if err := wsjson.Read(ctx, conn, &notification); err != nil {
		t.Fatalf("read notification: %v", err)
	}
	if notification.ID == "" {
		t.Fatal("got empty notification ID")
	}

	// Send ACK.
	ack := models.WSAck{Type: "ack", ID: notification.ID}
	if err := wsjson.Write(ctx, conn, ack); err != nil {
		t.Fatalf("write ack: %v", err)
	}

	// Wait for ACK to be processed server-side.
	time.Sleep(100 * time.Millisecond)

	// Verify pending is zero.
	info, err := env.rdb.XPending(ctx, "notifications:vack", "ws:vack").Result()
	if err != nil {
		t.Fatalf("XPending: %v", err)
	}
	if info.Count != 0 {
		t.Errorf("pending count = %d; want 0 after ACK", info.Count)
	}
}
