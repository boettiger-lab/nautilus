package main

import (
	"log"
	"net/http"
	"os"
)

// Config holds all runtime configuration sourced from environment variables.
type Config struct {
	// Upstream Ceph S3 endpoint, e.g. https://s3-west.nrp-nautilus.io
	UpstreamEndpoint string
	// Master credentials for the upstream Ceph S3
	UpstreamKey    string
	UpstreamSecret string
	// Region reported by Ceph; most Ceph deployments accept "us-east-1"
	UpstreamRegion string
	// Secret that protects the /admin API
	AdminSecret string
	// Path to the SQLite database file
	DBPath string
	// Address to listen on
	ListenAddr string
}

func configFromEnv() Config {
	cfg := Config{
		UpstreamEndpoint: getenv("UPSTREAM_ENDPOINT", "https://s3-west.nrp-nautilus.io"),
		UpstreamKey:      os.Getenv("UPSTREAM_ACCESS_KEY_ID"),
		UpstreamSecret:   os.Getenv("UPSTREAM_SECRET_KEY"),
		UpstreamRegion:   getenv("UPSTREAM_REGION", "us-east-1"),
		AdminSecret:      os.Getenv("ADMIN_SECRET"),
		DBPath:           getenv("DB_PATH", "/data/s3gw.db"),
		ListenAddr:       getenv("LISTEN_ADDR", ":8080"),
	}
	if cfg.UpstreamKey == "" || cfg.UpstreamSecret == "" {
		log.Fatal("UPSTREAM_ACCESS_KEY_ID and UPSTREAM_SECRET_KEY are required")
	}
	if cfg.AdminSecret == "" {
		log.Fatal("ADMIN_SECRET is required")
	}
	return cfg
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	cfg := configFromEnv()

	store, err := NewStore(cfg.DBPath)
	if err != nil {
		log.Fatalf("failed to open database: %v", err)
	}
	defer store.Close()

	proxy := NewProxy(cfg, store)
	admin := NewAdmin(cfg, store)

	mux := http.NewServeMux()
	// Admin API is on a distinct path prefix and is protected by X-Admin-Key.
	mux.Handle("/admin/", admin)
	// Everything else is an S3 request.
	mux.Handle("/", proxy)

	log.Printf("s3gw listening on %s, upstream %s", cfg.ListenAddr, cfg.UpstreamEndpoint)
	log.Fatal(http.ListenAndServe(cfg.ListenAddr, mux))
}
