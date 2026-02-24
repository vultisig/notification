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
}
type DatabaseConfig struct {
	Database string `mapstructure:"database" json:"database,omitempty"`
	User     string `mapstructure:"user" json:"user,omitempty"`
	Password string `mapstructure:"password" json:"password,omitempty"`
	Host     string `mapstructure:"host" json:"host,omitempty"`
	Port     int    `mapstructure:"port" json:"port,omitempty"`
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
	viper.SetDefault("database.database", "notification")
	viper.SetDefault("database.user", "root")
	viper.SetDefault("database.password", "password")
	viper.SetDefault("database.host", "localhost")
	viper.SetDefault("database.port", 3301)

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
