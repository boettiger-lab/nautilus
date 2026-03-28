package scraper

import (
	"log"
	"math"
	"sync"
	"time"

	"github.com/boettiger-lab/carbon-api/internal/carbon"
	"github.com/boettiger-lab/carbon-api/internal/prom"
)

// ModelMetrics holds the latest carbon and performance metrics for one LLM model.
type ModelMetrics struct {
	// Identity
	ModelName  string `json:"model_name"`
	Namespace  string `json:"namespace"`
	Container  string `json:"container"`
	GPUHardware string `json:"gpu_hardware"` // e.g. "A100-SXM4-80GB"
	Node       string `json:"node"`

	// Raw
	GPUCount        int     `json:"gpu_count"`
	PowerWatts      float64 `json:"power_watts"`
	TokensPerSec    float64 `json:"tokens_per_sec"`

	// Carbon
	CarbonIntensity      float64 `json:"carbon_intensity_kg_per_kwh"`        // grid intensity used
	CO2GramsPerHour      float64 `json:"co2_grams_per_hour"`
	CO2MgPerToken        float64 `json:"co2_mg_per_token,omitempty"`          // 0 when idle (5-min window, ≥5 tok/s)
	CO2MgPerTokenAvg24h  float64 `json:"co2_mg_per_token_avg_24h,omitempty"`  // running 24h mean, active periods only
	CO2MgPerTokenAvg7d   float64 `json:"co2_mg_per_token_avg_7d,omitempty"`   // running 7-day mean, active periods only

	UpdatedAt time.Time `json:"updated_at"`
}

// History is a fixed-size ring buffer of (time, value) pairs per metric.
type dataPoint struct {
	T time.Time
	V float64
}

type modelHistory struct {
	PowerWatts   []dataPoint
	CO2GramsPerHour []dataPoint
	CO2MgPerToken   []dataPoint
}

const maxHistory = 20160 // 7 days at 1-minute resolution (scrapeInterval=30s → 2 per min)

func (h *modelHistory) append(now time.Time, m *ModelMetrics) {
	push := func(buf *[]dataPoint, v float64) {
		*buf = append(*buf, dataPoint{T: now, V: v})
		if len(*buf) > maxHistory {
			*buf = (*buf)[len(*buf)-maxHistory:]
		}
	}
	push(&h.PowerWatts, m.PowerWatts)
	push(&h.CO2GramsPerHour, m.CO2GramsPerHour)
	if m.CO2MgPerToken > 0 {
		push(&h.CO2MgPerToken, m.CO2MgPerToken)
	}
}

// Scraper polls Prometheus and maintains in-memory state.
type Scraper struct {
	client   *prom.Client
	interval time.Duration

	mu      sync.RWMutex
	models  map[string]*ModelMetrics // key: namespace/container
	history map[string]*modelHistory
}

func New(promURL string, interval time.Duration) *Scraper {
	return &Scraper{
		client:   prom.NewClient(promURL, 30*time.Second),
		interval: interval,
		models:   make(map[string]*ModelMetrics),
		history:  make(map[string]*modelHistory),
	}
}

// Run starts the background scrape loop. Call in a goroutine.
func (s *Scraper) Run() {
	s.scrape()
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for range t.C {
		s.scrape()
	}
}

// Models returns a snapshot of all current model metrics.
func (s *Scraper) Models() []*ModelMetrics {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*ModelMetrics, 0, len(s.models))
	for _, m := range s.models {
		cp := *m
		out = append(out, &cp)
	}
	return out
}

// Series returns the history for a model/metric combination.
// metric is one of "power_watts", "co2_grams_per_hour", "co2_mg_per_token".
func (s *Scraper) Series(namespace, container, metric string, since time.Duration) [][2]interface{} {
	key := namespace + "/" + container
	s.mu.RLock()
	h, ok := s.history[key]
	s.mu.RUnlock()
	if !ok {
		return nil
	}

	cutoff := time.Now().Add(-since)
	var buf []dataPoint
	switch metric {
	case "power_watts":
		buf = h.PowerWatts
	case "co2_grams_per_hour":
		buf = h.CO2GramsPerHour
	case "co2_mg_per_token":
		buf = h.CO2MgPerToken
	default:
		return nil
	}

	var out [][2]interface{}
	for _, p := range buf {
		if p.T.After(cutoff) {
			out = append(out, [2]interface{}{p.T.Unix(), p.V})
		}
	}
	return out
}

// ---- internal ----

func (s *Scraper) scrape() {
	// 1. GPU power per pod (namespace + container)
	powerByKey, nodeByKey, err := s.queryPower()
	if err != nil {
		log.Printf("scraper: power query failed: %v", err)
	}

	// 2. GPU count + hardware model per pod
	gpuByKey, hardwareByKey, err := s.queryGPUInfo()
	if err != nil {
		log.Printf("scraper: gpu info query failed: %v", err)
	}

	// 3. Token generation rate per pod
	tokensByKey, modelNameByKey, err := s.queryTokens()
	if err != nil {
		log.Printf("scraper: token query failed: %v", err)
	}

	// Union of all known model keys
	keys := make(map[string]struct{})
	for k := range powerByKey {
		keys[k] = struct{}{}
	}
	for k := range tokensByKey {
		keys[k] = struct{}{}
	}

	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()

	for key := range keys {
		ns, container := splitKey(key)
		power := powerByKey[key]
		node := nodeByKey[key]
		intensity := carbon.IntensityForNode(node, ns)

		tokensPerSec := tokensByKey[key]
		modelName := modelNameByKey[key]
		gpuCount := gpuByKey[key]
		hw := hardwareByKey[key]

		co2PerHour := carbon.GramsPerHour(power, intensity)
		co2PerToken := 0.0
		if tokensPerSec > 5.0 {
			// Only compute CO₂/token at meaningful throughput (≥5 tok/s).
			// Below this threshold the ratio is dominated by the near-idle power
			// draw rather than the model's efficiency under real load.
			co2PerToken = carbon.MgPerToken(power, intensity, tokensPerSec)
		}

		m := &ModelMetrics{
			ModelName:       modelName,
			Namespace:       ns,
			Container:       container,
			GPUHardware:     hw,
			Node:            node,
			GPUCount:        gpuCount,
			PowerWatts:      math.Round(power*10) / 10,
			TokensPerSec:    math.Round(tokensPerSec*10) / 10,
			CarbonIntensity: intensity,
			CO2GramsPerHour: math.Round(co2PerHour*10) / 10,
			UpdatedAt:       now,
		}
		if co2PerToken > 0 {
			m.CO2MgPerToken = math.Round(co2PerToken*1000) / 1000
		}

		s.models[key] = m

		if s.history[key] == nil {
			s.history[key] = &modelHistory{}
		}
		h := s.history[key]
		h.append(now, m)

		// Running means of CO2/token (active periods only — zeros excluded by append).
		var sum24, sum7d float64
		var cnt24, cnt7d int
		cutoff24h := now.Add(-24 * time.Hour)
		cutoff7d := now.Add(-7 * 24 * time.Hour)
		for _, dp := range h.CO2MgPerToken {
			if dp.T.After(cutoff7d) {
				sum7d += dp.V
				cnt7d++
			}
			if dp.T.After(cutoff24h) {
				sum24 += dp.V
				cnt24++
			}
		}
		if cnt24 > 0 {
			m.CO2MgPerTokenAvg24h = math.Round(sum24/float64(cnt24)*1000) / 1000
		}
		if cnt7d > 0 {
			m.CO2MgPerTokenAvg7d = math.Round(sum7d/float64(cnt7d)*1000) / 1000
		}
	}
}

// queryPower returns total GPU power (W) keyed by "namespace/container".
// Also returns the node hostname (Hostname label) for carbon intensity lookup.
func (s *Scraper) queryPower() (map[string]float64, map[string]string, error) {
	// Include Hostname in the aggregation — all GPUs in a pod share the same node.
	results, err := s.client.Query(
		`sum by (namespace, container, Hostname) (avg_over_time(DCGM_FI_DEV_POWER_USAGE{namespace=~"nrp-llm|sdsc-llm"}[5m]))`,
	)
	if err != nil {
		return nil, nil, err
	}

	power := make(map[string]float64)
	nodes := make(map[string]string)
	for _, r := range results {
		key := r.Metric["namespace"] + "/" + r.Metric["container"]
		power[key] += r.Value
		if nodes[key] == "" {
			nodes[key] = r.Metric["Hostname"]
		}
	}
	return power, nodes, nil
}

// queryGPUInfo returns GPU count and hardware model keyed by "namespace/container".
func (s *Scraper) queryGPUInfo() (map[string]int, map[string]string, error) {
	results, err := s.client.Query(
		`count by (namespace, container, modelName) (DCGM_FI_DEV_GPU_UTIL{namespace=~"nrp-llm|sdsc-llm"})`,
	)
	if err != nil {
		return nil, nil, err
	}

	counts := make(map[string]int)
	hardware := make(map[string]string)
	for _, r := range results {
		key := r.Metric["namespace"] + "/" + r.Metric["container"]
		counts[key] += int(r.Value)
		if hardware[key] == "" {
			hardware[key] = r.Metric["modelName"]
		}
	}
	return counts, hardware, nil
}

// queryTokens returns 5-minute token generation rate keyed by "namespace/container".
// Also returns the LLM model_name label.
func (s *Scraper) queryTokens() (map[string]float64, map[string]string, error) {
	results, err := s.client.Query(
		`sum by (namespace, container, model_name) (rate(vllm:generation_tokens_total[5m]))`,
	)
	if err != nil {
		return nil, nil, err
	}

	tokens := make(map[string]float64)
	names := make(map[string]string)
	for _, r := range results {
		key := r.Metric["namespace"] + "/" + r.Metric["container"]
		tokens[key] += r.Value
		if names[key] == "" {
			names[key] = r.Metric["model_name"]
		}
	}
	return tokens, names, nil
}

// ClusterTimePoint is one time-step of aggregated cluster-wide carbon data.
type ClusterTimePoint struct {
	Timestamp       int64   `json:"t"`
	PowerWatts      float64 `json:"power_watts"`
	CO2GramsPerHour float64 `json:"co2_grams_per_hour"`
	CO2MgPerToken   float64 `json:"co2_mg_per_token,omitempty"` // 0 when no active generation
}

// ClusterTimeSeries queries Prometheus for historical power + token data, applies
// per-model carbon intensities, and returns aggregated cluster totals per time step.
func (s *Scraper) ClusterTimeSeries(rangeBack, step time.Duration) ([]ClusterTimePoint, error) {
	end := time.Now()
	start := end.Add(-rangeBack)

	// Fetch power and token rate time series in parallel.
	powerSeries, err := s.client.RangeQuery(
		`sum by (namespace, container) (DCGM_FI_DEV_POWER_USAGE{namespace=~"nrp-llm|sdsc-llm"})`,
		start, end, step,
	)
	if err != nil {
		return nil, err
	}
	tokenSeries, err := s.client.RangeQuery(
		`sum by (namespace, container) (rate(vllm:generation_tokens_total[5m]))`,
		start, end, step,
	)
	if err != nil {
		return nil, err
	}

	// Build intensity lookup from current model state.
	s.mu.RLock()
	intensityByKey := make(map[string]float64, len(s.models))
	for key, m := range s.models {
		intensityByKey[key] = m.CarbonIntensity
	}
	s.mu.RUnlock()

	type agg struct{ power, co2, tokens float64 }
	byTime := make(map[int64]*agg)

	for _, sr := range powerSeries {
		key := sr.Metric["namespace"] + "/" + sr.Metric["container"]
		intensity, ok := intensityByKey[key]
		if !ok {
			intensity = carbon.USAverage
		}
		for _, pt := range sr.Points {
			ts := pt.Time.Unix()
			if byTime[ts] == nil {
				byTime[ts] = &agg{}
			}
			byTime[ts].power += pt.Value
			byTime[ts].co2 += carbon.GramsPerHour(pt.Value, intensity)
		}
	}
	for _, sr := range tokenSeries {
		for _, pt := range sr.Points {
			ts := pt.Time.Unix()
			if byTime[ts] == nil {
				byTime[ts] = &agg{}
			}
			byTime[ts].tokens += pt.Value
		}
	}

	// Sort by timestamp and return.
	out := make([]ClusterTimePoint, 0, len(byTime))
	for ts, a := range byTime {
		pt := ClusterTimePoint{
			Timestamp:       ts,
			PowerWatts:      math.Round(a.power*10) / 10,
			CO2GramsPerHour: math.Round(a.co2*10) / 10,
		}
		// CO2/token = total_co2_grams_per_hour / (tokens_per_sec × 3600) × 1000 mg/g
		if a.tokens > 0.1 {
			pt.CO2MgPerToken = math.Round(a.co2/a.tokens/3.6*1000) / 1000
		}
		out = append(out, pt)
	}
	// Simple insertion sort — range queries are usually small.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Timestamp < out[j-1].Timestamp; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, nil
}

func splitKey(key string) (namespace, container string) {
	for i, c := range key {
		if c == '/' {
			return key[:i], key[i+1:]
		}
	}
	return key, ""
}
