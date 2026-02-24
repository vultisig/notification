package main

import (
	"log"

	"github.com/DataDog/datadog-go/statsd"
	"github.com/hibiken/asynq"
	"github.com/vultisig/notification/api"
	"github.com/vultisig/notification/cache"
	"github.com/vultisig/notification/config"
	"github.com/vultisig/notification/storage"
)

func main() {
	cfg, err := config.GetConfigure()
	if err != nil {
		panic(err)
	}
	_ = cfg
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

	redisOptions := asynq.RedisClientOpt{
		Addr:     cfg.Redis.Host + ":" + cfg.Redis.Port,
		Username: cfg.Redis.User,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
	}
	client := asynq.NewClient(redisOptions)

	defer func() {
		if err := cacheClient.Close(); err != nil {
			log.Printf("fail to close redis client,err: %v", err)
		}
		if err := client.Close(); err != nil {
			log.Printf("fail to close asynq client,err: %v", err)
		}
	}()
	apiServer, err := api.NewServer(cfg.Server.Port, sdClient, db, client, cacheClient, cfg.VAPIDPublicKey)
	if err != nil {
		panic(err)
	}
	if err := apiServer.StartServer(); err != nil {
		panic(err)
	}
}
