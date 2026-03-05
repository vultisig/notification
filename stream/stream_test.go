package stream_test

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/vultisig/notification/stream"
)

func newTestStore(t *testing.T, ttl time.Duration) (*stream.Store, *redis.Client, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })
	return stream.NewStore(rdb, ttl), rdb, mr
}

func TestPublishSubscribe(t *testing.T) {
	store, _, _ := newTestStore(t, 60*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := store.Subscribe(ctx, "vault1", "consumer1")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	if err := store.Publish(ctx, "vault1", stream.PublishRequest{
		VaultName:  "My Vault",
		QRCodeData: "qr-data",
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	select {
	case msg := <-ch:
		if msg.VaultName != "My Vault" {
			t.Errorf("VaultName = %q; want %q", msg.VaultName, "My Vault")
		}
		if msg.QRCodeData != "qr-data" {
			t.Errorf("QRCodeData = %q; want %q", msg.QRCodeData, "qr-data")
		}
		if msg.ID == "" {
			t.Error("ID is empty")
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for message")
	}
}

func TestAck(t *testing.T) {
	store, rdb, _ := newTestStore(t, 60*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := store.Subscribe(ctx, "vault2", "consumer1")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	if err := store.Publish(ctx, "vault2", stream.PublishRequest{
		VaultName:  "V",
		QRCodeData: "q",
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	var msgID string
	select {
	case msg := <-ch:
		msgID = msg.ID
	case <-ctx.Done():
		t.Fatal("timed out waiting for message")
	}

	if err := store.Ack(ctx, "vault2", msgID); err != nil {
		t.Fatalf("Ack: %v", err)
	}

	// Verify pending count is zero.
	info, err := rdb.XPending(ctx, "notifications:vault2", "ws:vault2").Result()
	if err != nil {
		t.Fatalf("XPending: %v", err)
	}
	if info.Count != 0 {
		t.Errorf("pending count = %d; want 0", info.Count)
	}
}

func TestContextCancel(t *testing.T) {
	store, _, _ := newTestStore(t, 60*time.Second)
	ctx, cancel := context.WithCancel(context.Background())

	ch, err := store.Subscribe(ctx, "vault3", "consumer1")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	cancel()

	// Channel should close after context is cancelled.
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("expected channel to be closed after cancel")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("channel not closed after context cancel")
	}
}

func TestStalePendingSkipped(t *testing.T) {
	ttl := 2 * time.Second
	store, rdb, mr := newTestStore(t, ttl)

	bgCtx := context.Background()

	// Create the consumer group by subscribing.
	ctx1, cancel1 := context.WithCancel(bgCtx)
	ch, err := store.Subscribe(ctx1, "vault4", "consumer1")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	cancel1()
	for range ch {
	} // drain

	// Publish a message.
	if err := store.Publish(bgCtx, "vault4", stream.PublishRequest{
		VaultName:  "Stale",
		QRCodeData: "old",
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	// Manually claim the message as pending by reading it with a separate subscriber,
	// then NOT acking it, then fast-forwarding time.
	ctx2, cancel2 := context.WithTimeout(bgCtx, 3*time.Second)
	defer cancel2()
	ch2, err := store.Subscribe(ctx2, "vault4", "consumer1")
	if err != nil {
		t.Fatalf("Subscribe second: %v", err)
	}
	select {
	case <-ch2: // receive but don't ack
	case <-ctx2.Done():
		t.Fatal("timed out waiting for message before fast-forward")
	}
	cancel2()
	for range ch2 {
	}

	// Check it's pending.
	info, err := rdb.XPending(bgCtx, "notifications:vault4", "ws:vault4").Result()
	if err != nil {
		t.Fatalf("XPending: %v", err)
	}
	if info.Count != 1 {
		t.Fatalf("expected 1 pending before fast-forward, got %d", info.Count)
	}

	// Fast-forward past TTL so the message timestamp is stale.
	mr.FastForward(ttl + time.Second)

	// Reconnect — stale pending should be auto-acked and not delivered.
	ctx3, cancel3 := context.WithTimeout(bgCtx, 3*time.Second)
	defer cancel3()
	ch3, err := store.Subscribe(ctx3, "vault4", "consumer1")
	if err != nil {
		t.Fatalf("Subscribe third: %v", err)
	}

	select {
	case msg, ok := <-ch3:
		if ok {
			t.Errorf("expected no messages, got: %+v", msg)
		}
	case <-time.After(2 * time.Second):
		// No message in 2s — correct.
	}

	// Pending should now be zero.
	info2, err := rdb.XPending(bgCtx, "notifications:vault4", "ws:vault4").Result()
	if err != nil {
		t.Fatalf("XPending after reconnect: %v", err)
	}
	if info2.Count != 0 {
		t.Errorf("stale message not acked: pending = %d", info2.Count)
	}

	cancel3()
}

func TestPublishMultipleVaults(t *testing.T) {
	store, _, _ := newTestStore(t, 60*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch1, err := store.Subscribe(ctx, "vaultA", "consumer1")
	if err != nil {
		t.Fatalf("Subscribe vaultA: %v", err)
	}
	ch2, err := store.Subscribe(ctx, "vaultB", "consumer1")
	if err != nil {
		t.Fatalf("Subscribe vaultB: %v", err)
	}

	if err := store.Publish(ctx, "vaultA", stream.PublishRequest{VaultName: "A", QRCodeData: "a"}); err != nil {
		t.Fatalf("Publish vaultA: %v", err)
	}
	if err := store.Publish(ctx, "vaultB", stream.PublishRequest{VaultName: "B", QRCodeData: "b"}); err != nil {
		t.Fatalf("Publish vaultB: %v", err)
	}

	got := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case msg := <-ch1:
			got[msg.VaultName] = true
		case msg := <-ch2:
			got[msg.VaultName] = true
		case <-ctx.Done():
			t.Fatalf("timed out on iteration %d", i)
		}
	}

	if !got["A"] || !got["B"] {
		t.Errorf("expected messages for both vaults, got: %v", got)
	}
}
