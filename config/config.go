package config

import (
	"fmt"

	"github.com/spf13/viper"
)

type Config struct {
	Server struct {
		Port int64  `mapstructure:"port" json:"port,omitempty"`
		Host string `mapstructure:"host" json:"host,omitempty"`
	}
	Database            DatabaseConfig `mapstructure:"database" json:"database,omitempty"`
	Redis               RedisConfig    `mapstructure:"redis" json:"redis,omitempty"`
	Certificate         string         `mapstructure:"certificate" json:"certificate,omitempty"`
	CertificatePassword string         `mapstructure:"certificate-password" json:"certificate-password,omitempty"`
	Production          bool           `mapstructure:"production" json:"production,omitempty"`
	FirebaseCredentials string         `mapstructure:"firebase-credentials" json:"firebase-credentials,omitempty"`
	VAPIDPublicKey      string         `mapstructure:"vapid-public-key" json:"vapid-public-key,omitempty"`
	VAPIDPrivateKey     string         `mapstructure:"vapid-private-key" json:"vapid-private-key,omitempty"`
	VAPIDSubscriber     string         `mapstructure:"vapid-subscriber" json:"vapid-subscriber,omitempty"`
}
type DatabaseConfig struct {
	DSN string `mapstructure:"dsn" json:"dsn,omitempty"`
}

type RedisConfig struct {
	Host     string `mapstructure:"host" json:"host,omitempty"`
	Port     string `mapstructure:"port" json:"port,omitempty"`
	User     string `mapstructure:"user" json:"user,omitempty"`
	Password string `mapstructure:"password" json:"password,omitempty"`
	DB       int    `mapstructure:"db" json:"db,omitempty"`
}

func GetConfigure() (*Config, error) {
	viper.SetConfigName("config")
	viper.AddConfigPath(".")
	viper.AutomaticEnv()

	viper.SetDefault("server.port", 8080)
	viper.SetDefault("server.host", "localhost")
	viper.SetDefault("database.dsn", "postgres://localhost:5432/notification")

	if err := viper.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("fail to reading config file, %w", err)
	}
	var cfg Config
	err := viper.Unmarshal(&cfg)
	if err != nil {
		return nil, fmt.Errorf("unable to decode into struct, %w", err)
	}
	return &cfg, nil
}
