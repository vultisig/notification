package stream

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"
)

type Store struct {
	rdb        *redis.Client
	messageTTL time.Duration
	logger     *logrus.Entry
}

type Message struct {
	ID         string
	VaultName  string
	QRCodeData string
}

type PublishRequest struct {
	VaultName  string
	QRCodeData string
}

func NewStore(rdb *redis.Client, messageTTL time.Duration) *Store {
	return &Store{
		rdb:        rdb,
		messageTTL: messageTTL,
		logger:     logrus.WithField("module", "stream"),
	}
}

func streamKey(vaultID string) string   { return "notifications:" + vaultID }
func groupName(vaultID string) string   { return "ws:" + vaultID }

// Publish writes a message to the vault's stream and trims expired entries.
func (s *Store) Publish(ctx context.Context, vaultID string, req PublishRequest) error {
	minID := fmt.Sprintf("%d-0", time.Now().Add(-s.messageTTL).UnixMilli())
	_, err := s.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: streamKey(vaultID),
		MinID:  "~" + minID,
		Values: map[string]interface{}{
			"vault_name":   req.VaultName,
			"qr_code_data": req.QRCodeData,
		},
	}).Result()
	if err != nil {
		return fmt.Errorf("stream publish: %w", err)
	}
	return nil
}

// Subscribe returns a channel that yields messages for the given vault.
// The consumer name should be unique per connection (e.g. auth_token_hash[:16]).
// The channel is closed when ctx is cancelled.
func (s *Store) Subscribe(ctx context.Context, vaultID, consumerName string) (<-chan Message, error) {
	key := streamKey(vaultID)
	group := groupName(vaultID)

	// Create consumer group if it doesn't exist (idempotent).
	err := s.rdb.XGroupCreateMkStream(ctx, key, group, "0").Err()
	if err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		return nil, fmt.Errorf("stream group create: %w", err)
	}

	ch := make(chan Message, 16)
	go s.subscribeLoop(ctx, ch, key, group, consumerName)
	return ch, nil
}

func (s *Store) subscribeLoop(ctx context.Context, ch chan<- Message, key, group, consumer string) {
	defer close(ch)

	// Phase 1: deliver pending (unacked) messages, skip stale ones.
	s.deliverPending(ctx, ch, key, group, consumer)

	// Phase 2: block-wait for new messages.
	for {
		if ctx.Err() != nil {
			return
		}
		streams, err := s.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    group,
			Consumer: consumer,
			Streams:  []string{key, ">"},
			Count:    1,
			Block:    5 * time.Second,
		}).Result()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if err == redis.Nil {
				continue // timeout, no new messages
			}
			s.logger.WithError(err).Error("stream read")
			time.Sleep(time.Second) // back off on unexpected errors
			continue
		}
		for _, stream := range streams {
			for _, msg := range stream.Messages {
				m := parseMessage(msg)
				select {
				case ch <- m:
				case <-ctx.Done():
					return
				}
			}
		}
	}
}

func (s *Store) deliverPending(ctx context.Context, ch chan<- Message, key, group, consumer string) {
	cutoff := time.Now().Add(-s.messageTTL)
	startID := "0"
	for {
		if ctx.Err() != nil {
			return
		}
		streams, err := s.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    group,
			Consumer: consumer,
			Streams:  []string{key, startID},
			Count:    100,
		}).Result()
		if err != nil {
			if err != redis.Nil {
				s.logger.WithError(err).Error("stream read pending")
			}
			return
		}
		if len(streams) == 0 || len(streams[0].Messages) == 0 {
			return // no more pending
		}
		for _, msg := range streams[0].Messages {
			ts := messageTimestamp(msg.ID)
			if ts.Before(cutoff) {
				// Stale message — auto-ACK and skip.
				s.rdb.XAck(ctx, key, group, msg.ID)
				continue
			}
			m := parseMessage(msg)
			select {
			case ch <- m:
			case <-ctx.Done():
				return
			}
			startID = msg.ID // advance cursor
		}
	}
}

// Ack acknowledges a message so it won't be re-delivered.
func (s *Store) Ack(ctx context.Context, vaultID, messageID string) error {
	return s.rdb.XAck(ctx, streamKey(vaultID), groupName(vaultID), messageID).Err()
}

func (s *Store) Close() error {
	return s.rdb.Close()
}

func parseMessage(msg redis.XMessage) Message {
	return Message{
		ID:         msg.ID,
		VaultName:  stringVal(msg.Values, "vault_name"),
		QRCodeData: stringVal(msg.Values, "qr_code_data"),
	}
}

func stringVal(m map[string]interface{}, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// messageTimestamp extracts the Unix millisecond timestamp from a Redis stream ID (e.g. "1234567890123-0").
func messageTimestamp(id string) time.Time {
	parts := strings.SplitN(id, "-", 2)
	if len(parts) == 0 {
		return time.Time{}
	}
	ms, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}
