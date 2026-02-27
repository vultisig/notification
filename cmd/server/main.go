package main

import (
	"log"
	"time"

	"github.com/DataDog/datadog-go/statsd"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"github.com/vultisig/notification/api"
	"github.com/vultisig/notification/cache"
	"github.com/vultisig/notification/config"
	"github.com/vultisig/notification/storage"
	"github.com/vultisig/notification/stream"
	"github.com/vultisig/notification/ws"
)

func main() {
	cfg, err := config.GetConfigure()
	if err != nil {
		panic(err)
	}

	sdClient, err := statsd.New("127.0.0.1:8125")
	if err != nil {
		panic(err)
	}

	db, err := storage.NewDatabase(&cfg.Database)
	if err != nil {
		panic(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			panic(err)
		}
	}()

	cacheClient, err := cache.NewRedisStorage(cfg.Redis)
	if err != nil {
		panic(err)
	}

	redisAddr := cfg.Redis.Host + ":" + cfg.Redis.Port
	redisOptions := asynq.RedisClientOpt{
		Addr:     redisAddr,
		Username: cfg.Redis.User,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
	}
	asynqClient := asynq.NewClient(redisOptions)

	// Separate Redis client for stream operations (clean ownership, no shared state with cache).
	streamRedis := redis.NewClient(&redis.Options{
		Addr:     redisAddr,
		Username: cfg.Redis.User,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
	})

	messageTTL := time.Duration(cfg.Stream.MessageTTL) * time.Second
	if messageTTL == 0 {
		messageTTL = 60 * time.Second
	}
	streamStore := stream.NewStore(streamRedis, messageTTL)

	wsHandler := ws.NewHandler(streamStore, db, streamRedis)

	defer func() {
		if err := cacheClient.Close(); err != nil {
			log.Printf("fail to close redis client, err: %v", err)
		}
		if err := asynqClient.Close(); err != nil {
			log.Printf("fail to close asynq client, err: %v", err)
		}
		if err := streamStore.Close(); err != nil {
			log.Printf("fail to close stream store, err: %v", err)
		}
	}()

	apiServer, err := api.NewServer(cfg.Server.Port, sdClient, db, asynqClient, cacheClient, streamStore, wsHandler, cfg.VAPIDPublicKey)
	if err != nil {
		panic(err)
	}
	if err := apiServer.StartServer(); err != nil {
		panic(err)
	}
}
