package main

import (
	"encoding/json"
	_ "embed"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/boettiger-lab/carbon-api/internal/scraper"
)

//go:embed static/dashboard.html
var dashboardHTML []byte

//go:embed static/methodology.html
var methodologyHTML []byte

var s *scraper.Scraper

func main() {
	promURL := getenv("PROMETHEUS_URL", "https://prometheus.nrp-nautilus.io")
	interval := getenvDuration("SCRAPE_INTERVAL", 30*time.Second)
	addr := getenv("LISTEN_ADDR", ":8080")

	s = scraper.New(promURL, interval)
	go s.Run()

	mux := http.NewServeMux()
	mux.HandleFunc("/", handleDashboard)
	mux.HandleFunc("/methodology", handleMethodology)
	mux.HandleFunc("/api/v1/carbon", handleModels)
	mux.HandleFunc("/api/v1/carbon/timeseries", handleTimeSeries)
	mux.HandleFunc("/api/v1/carbon/", handleSeries) // /api/v1/carbon/{ns}/{container}/{metric}
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})

	log.Printf("carbon-api listening on %s (prometheus=%s, interval=%s)", addr, promURL, interval)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}

func handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(dashboardHTML)
}

func handleMethodology(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(methodologyHTML)
}

// GET /api/v1/carbon/timeseries?range=24h|7d|30d
// Returns aggregated cluster-wide CO2 and power time series from Prometheus history.
func handleTimeSeries(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rangeStr := r.URL.Query().Get("range")
	rangeDur := parseDuration(rangeStr, 24*time.Hour)

	// Choose step size to keep response ≤ ~500 points.
	step := rangeDur / 400
	if step < time.Minute {
		step = time.Minute
	}
	// Round to clean intervals.
	switch {
	case rangeDur <= 25*time.Hour:
		step = 5 * time.Minute
	case rangeDur <= 8*24*time.Hour:
		step = time.Hour
	default:
		step = 6 * time.Hour
	}

	pts, err := s.ClusterTimeSeries(rangeDur, step)
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"range":  rangeDur.String(),
		"step":   step.String(),
		"points": pts,
		"error":  errMsg,
	})
}

// GET /api/v1/carbon
// Returns current metrics for all models, sorted by CO2/hr descending.
func handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	models := s.Models()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"models":     models,
		"updated_at": time.Now(),
	})
}

// GET /api/v1/carbon/{namespace}/{container}/{metric}?range=1h|6h|24h|48h
// metric: power_watts | co2_grams_per_hour | co2_mg_per_token
func handleSeries(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// strip prefix /api/v1/carbon/
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/carbon/")
	parts := strings.SplitN(path, "/", 3)
	if len(parts) != 3 {
		http.Error(w, "usage: /api/v1/carbon/{namespace}/{container}/{metric}", http.StatusBadRequest)
		return
	}
	ns, container, metric := parts[0], parts[1], parts[2]

	rangeStr := r.URL.Query().Get("range")
	rangeDur := parseDuration(rangeStr, 24*time.Hour)

	series := s.Series(ns, container, metric, rangeDur)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"namespace": ns,
		"container": container,
		"metric":    metric,
		"range":     rangeDur.String(),
		"series":    series,
	})
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getenvDuration(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	// accept bare seconds integer or Go duration string
	if n, err := strconv.Atoi(v); err == nil {
		return time.Duration(n) * time.Second
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}

func parseDuration(s string, def time.Duration) time.Duration {
	if s == "" {
		return def
	}
	// time.ParseDuration only handles up to "h"; handle "d" manually.
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err == nil {
			return time.Duration(n) * 24 * time.Hour
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return def
	}
	return d
}
