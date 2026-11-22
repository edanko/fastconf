// Command basic demonstrates loading a configuration file into a struct.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/edanko/fastconf"
)

type Config struct {
	Domain   string         `mapstructure:"domain"`
	Database DatabaseConfig `mapstructure:"database"`
	Features FeaturesConfig `mapstructure:"features"`
}

type DatabaseConfig struct {
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	User     string `mapstructure:"user"`
	Password string `mapstructure:"password"`
}

type FeaturesConfig struct {
	Debug     bool `mapstructure:"debug"`
	RateLimit int  `mapstructure:"rateLimit"`
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	loader, err := fastconf.New(
		fastconf.WithConfigPath("config.yaml"),
		fastconf.WithLogger(logger),
	)
	if err != nil {
		logger.Error("create loader", "error", err)
		os.Exit(1)
	}
	defer func() { _ = loader.Close() }()

	var config Config
	if err := loader.Decode(context.Background(), &config); err != nil {
		logger.Error("decode configuration", "error", err)
		return
	}
	logger.Info("configuration loaded",
		"domain", config.Domain,
		"database", config.Database.Host,
		"port", config.Database.Port,
		"debug", config.Features.Debug,
	)
}
