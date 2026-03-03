package api

import (
	"context"
	"encoding/json"

	"github.com/vultisig/notification/models"
)

func (s *Server) StartRedisSubscriber(ctx context.Context) {
	sub := s.redisClient.Subscribe(ctx, models.WSNotifyChannel)
	defer sub.Close()
	s.logger.Infof("redis subscriber: listening on channel %s", models.WSNotifyChannel)

	ch := sub.Channel()
	for {
		select {
		case <-ctx.Done():
			s.logger.Info("redis subscriber: stopping")
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			var req models.NotificationRequest
			if err := json.Unmarshal([]byte(msg.Payload), &req); err != nil {
				s.logger.Errorf("redis subscriber: failed to unmarshal message: %v", err)
				continue
			}
			payload, err := json.Marshal(req)
			if err != nil {
				s.logger.Errorf("redis subscriber: failed to marshal broadcast payload: %v", err)
				continue
			}
			s.wsHub.Broadcast(req.VaultId, req.LocalPartyId, payload)
		}
	}
}
