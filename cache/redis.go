package cache

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/vultisig/notification/config"
	"github.com/vultisig/notification/contexthelper"
)

type RedisStorage struct {
	cfg    config.RedisConfig
	client *redis.Client
}

func NewRedisStorage(cfg config.RedisConfig) (*RedisStorage, error) {
	client := redis.NewClient(&redis.Options{
		Addr:     cfg.Host + ":" + cfg.Port,
		Username: cfg.User,
		Password: cfg.Password,
		DB:       cfg.DB,
	})
	status := client.Ping(context.Background())
	if status.Err() != nil {
		return nil, status.Err()
	}
	return &RedisStorage{
		cfg:    cfg,
		client: client,
	}, nil
}

func (r *RedisStorage) Get(ctx context.Context, key string) (string, error) {
	if err := contexthelper.CheckCancellation(ctx); err != nil {
		return "", err
	}
	return r.client.Get(ctx, key).Result()
}
func (r *RedisStorage) Set(ctx context.Context, key string, value string, expiry time.Duration) error {
	if err := contexthelper.CheckCancellation(ctx); err != nil {
		return err
	}
	return r.client.Set(ctx, key, value, expiry).Err()
}
func (r *RedisStorage) Expire(ctx context.Context, key string, expiry time.Duration) error {
	if err := contexthelper.CheckCancellation(ctx); err != nil {
		return err
	}
	return r.client.Expire(ctx, key, expiry).Err()
}
func (r *RedisStorage) Delete(ctx context.Context, key string) error {
	if err := contexthelper.CheckCancellation(ctx); err != nil {
		return err
	}
	return r.client.Del(ctx, key).Err()
}
func (r *RedisStorage) Client() *redis.Client {
	return r.client
}

func (r *RedisStorage) Close() error {
	return r.client.Close()
}
