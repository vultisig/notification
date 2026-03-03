package config

import (
	"fmt"
	"reflect"
	"strings"

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
	URI      string `mapstructure:"uri" json:"uri,omitempty"`
	Host     string `mapstructure:"host" json:"host,omitempty"`
	Port     string `mapstructure:"port" json:"port,omitempty"`
	User     string `mapstructure:"user" json:"user,omitempty"`
	Password string `mapstructure:"password" json:"password,omitempty"`
	DB       int    `mapstructure:"db" json:"db,omitempty"`
}

func (r RedisConfig) UseURI() bool {
	return r.URI != ""
}

func GetConfigure() (*Config, error) {
	addKeysToViper(viper.GetViper(), reflect.TypeOf(Config{}))
	viper.SetConfigName("config")
	viper.AddConfigPath(".")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))
	viper.AutomaticEnv()

	viper.SetDefault("server.port", 8080)
	viper.SetDefault("server.host", "localhost")
	viper.SetDefault("database.dsn", "postgres://localhost:5432/notification")

	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("fail to reading config file, %w", err)
		}
		// This is expected for ENV based config, swallow the error
	}
	var cfg Config
	err := viper.Unmarshal(&cfg)
	if err != nil {
		return nil, fmt.Errorf("unable to decode into struct, %w", err)
	}
	return &cfg, nil
}

func addKeysToViper(v *viper.Viper, t reflect.Type) {
	keys := getAllKeys(t)
	for _, key := range keys {
		v.SetDefault(key, "")
	}
}

func getAllKeys(t reflect.Type) []string {
	var result []string

	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)

		// Try mapstructure tag first
		tagName := f.Tag.Get("mapstructure")
		if tagName == "" || tagName == "-" {
			// Fallback to JSON tag
			jsonTag := f.Tag.Get("json")
			if jsonTag != "" && jsonTag != "-" {
				// Handle comma-separated options (e.g., "field_name,omitempty")
				tagName = strings.Split(jsonTag, ",")[0]
			}
		} else {
			// Handle comma-separated options in mapstructure tag
			tagName = strings.Split(tagName, ",")[0]
		}

		// Final fallback to field name if no valid tags found
		if tagName == "" || tagName == "-" {
			tagName = f.Name
		}

		n := strings.ToUpper(tagName)

		if reflect.Struct == f.Type.Kind() {
			subKeys := getAllKeys(f.Type)
			for _, k := range subKeys {
				result = append(result, n+"."+k)
			}
		} else {
			result = append(result, n)
		}
	}

	return result
}
