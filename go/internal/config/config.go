// Package config reads settings from environment variables. In docker these
// come from the .env file through docker compose, so the token never ends up
// in the image or in git.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	CanvasURL   string
	CanvasToken string
	DatabaseURL string
	DataDir     string // downloaded files go in DataDir/courses

	ListenAddr   string
	APIToken     string // optional, if set every request needs it
	SyncInterval time.Duration
	DueSoon      time.Duration // how far ahead "due soon" alerts look
	Workers      int           // courses synced at the same time
	WebhookURL   string        // optional, changes get POSTed here
	Location     *time.Location
}

func Load() (*Config, error) {
	c := &Config{
		CanvasURL:   strings.TrimRight(os.Getenv("CANVAS_BASE_URL"), "/"),
		CanvasToken: os.Getenv("CANVAS_TOKEN"),
		DatabaseURL: os.Getenv("DATABASE_URL"),
		DataDir:     env("DATA_DIR", "./data"),
		ListenAddr:  env("LISTEN_ADDR", ":8080"),
		APIToken:    os.Getenv("API_TOKEN"),
		WebhookURL:  os.Getenv("WEBHOOK_URL"),
	}
	var err error
	if c.SyncInterval, err = time.ParseDuration(env("SYNC_INTERVAL", "30m")); err != nil {
		return nil, fmt.Errorf("SYNC_INTERVAL: %w", err)
	}
	if c.DueSoon, err = time.ParseDuration(env("DUE_SOON", "48h")); err != nil {
		return nil, fmt.Errorf("DUE_SOON: %w", err)
	}
	if c.Workers, err = strconv.Atoi(env("SYNC_WORKERS", "3")); err != nil || c.Workers < 1 {
		return nil, fmt.Errorf("SYNC_WORKERS should be a number >= 1")
	}
	// used when someone asks for "2026-11-20" without a time
	if c.Location, err = time.LoadLocation(env("TZ", "America/Edmonton")); err != nil {
		return nil, fmt.Errorf("TZ: %w", err)
	}
	if c.DatabaseURL == "" {
		c.DatabaseURL = postgresURL()
	}
	if c.DatabaseURL == "" {
		return nil, fmt.Errorf("set DATABASE_URL, or POSTGRES_PASSWORD (+ POSTGRES_HOST/USER/DB)")
	}
	return c, nil
}

// CheckCanvas is separate because the api can run without canvas access.
func (c *Config) CheckCanvas() error {
	if c.CanvasURL == "" || c.CanvasToken == "" {
		return fmt.Errorf("set CANVAS_BASE_URL and CANVAS_TOKEN in .env (see .env.example)")
	}
	return nil
}

// postgresURL builds the connection string from the same POSTGRES_* vars the
// postgres image uses, so the password only has to live in .env
func postgresURL() string {
	pw := os.Getenv("POSTGRES_PASSWORD")
	if pw == "" {
		return ""
	}
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(env("POSTGRES_USER", "ace"), pw),
		Host:     env("POSTGRES_HOST", "localhost") + ":" + env("POSTGRES_PORT", "5432"),
		Path:     env("POSTGRES_DB", "ace"),
		RawQuery: "sslmode=disable",
	}
	return u.String()
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
