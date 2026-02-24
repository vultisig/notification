package main

import (
	"fmt"

	"github.com/DataDog/datadog-go/statsd"
	"github.com/hibiken/asynq"
	"github.com/sirupsen/logrus"
	"github.com/vultisig/notification/config"
	"github.com/vultisig/notification/models"
	"github.com/vultisig/notification/service"
	"github.com/vultisig/notification/storage"
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

	redisOptions := asynq.RedisClientOpt{
		Addr:     cfg.Redis.Host + ":" + cfg.Redis.Port,
		Username: cfg.Redis.User,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
	}
	db, err := storage.NewDatabase(&cfg.Database)
	if err != nil {
		panic(err)
	}

	workerServce, err := service.NewNotificationService(sdClient, db, "https://api.vultisig.com", cfg.Certificate, cfg.CertificatePassword, cfg.Production, cfg.FirebaseCredentials)
	if err != nil {
		panic(err)
	}

	srv := asynq.NewServer(
		redisOptions,
		asynq.Config{
			Logger:      logrus.StandardLogger(),
			Concurrency: 10,
			Queues: map[string]int{
				models.QUEUE_NAME: 10,
			},
		},
	)

	// mux maps a type to a handler
	mux := asynq.NewServeMux()
	mux.HandleFunc(models.TypeNotification, workerServce.HandleNotification)

	if err := srv.Run(mux); err != nil {
		panic(fmt.Errorf("could not run server: %w", err))
	}
}
