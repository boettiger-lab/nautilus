package prom

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type Client struct {
	baseURL string
	http    *http.Client
}

func NewClient(baseURL string, timeout time.Duration) *Client {
	return &Client{
		baseURL: baseURL,
		http:    &http.Client{Timeout: timeout},
	}
}

type Result struct {
	Metric map[string]string
	Value  float64
	Time   time.Time
}

type response struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Value  [2]json.RawMessage `json:"value"`
		} `json:"result"`
	} `json:"data"`
	Error string `json:"error"`
}

// RangePoint is one timestamp→value sample from a range query.
type RangePoint struct {
	Time  time.Time
	Value float64
}

// RangeSeries is one labeled series from a range query.
type RangeSeries struct {
	Metric map[string]string
	Points []RangePoint
}

type rangeResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string   `json:"metric"`
			Values [][2]json.RawMessage `json:"values"`
		} `json:"result"`
	} `json:"data"`
	Error string `json:"error"`
}

// RangeQuery executes a Prometheus range query and returns one series per label-set.
func (c *Client) RangeQuery(query string, start, end time.Time, step time.Duration) ([]RangeSeries, error) {
	u := fmt.Sprintf("%s/api/v1/query_range?query=%s&start=%d&end=%d&step=%d",
		c.baseURL,
		url.QueryEscape(query),
		start.Unix(), end.Unix(), int(step.Seconds()),
	)
	resp, err := c.http.Get(u)
	if err != nil {
		return nil, fmt.Errorf("prometheus range query: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var pr rangeResponse
	if err := json.Unmarshal(body, &pr); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}
	if pr.Status != "success" {
		return nil, fmt.Errorf("prometheus error: %s", pr.Error)
	}

	out := make([]RangeSeries, 0, len(pr.Data.Result))
	for _, r := range pr.Data.Result {
		pts := make([]RangePoint, 0, len(r.Values))
		for _, v := range r.Values {
			var ts float64
			if err := json.Unmarshal(v[0], &ts); err != nil {
				continue
			}
			var valStr string
			if err := json.Unmarshal(v[1], &valStr); err != nil {
				continue
			}
			val, err := strconv.ParseFloat(valStr, 64)
			if err != nil || val != val {
				continue
			}
			pts = append(pts, RangePoint{Time: time.Unix(int64(ts), 0), Value: val})
		}
		out = append(out, RangeSeries{Metric: r.Metric, Points: pts})
	}
	return out, nil
}

func (c *Client) Query(query string) ([]Result, error) {
	u := fmt.Sprintf("%s/api/v1/query?query=%s", c.baseURL, url.QueryEscape(query))
	resp, err := c.http.Get(u)
	if err != nil {
		return nil, fmt.Errorf("prometheus query: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var pr response
	if err := json.Unmarshal(body, &pr); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}
	if pr.Status != "success" {
		return nil, fmt.Errorf("prometheus error: %s", pr.Error)
	}

	results := make([]Result, 0, len(pr.Data.Result))
	for _, r := range pr.Data.Result {
		var ts float64
		if err := json.Unmarshal(r.Value[0], &ts); err != nil {
			continue
		}
		var valStr string
		if err := json.Unmarshal(r.Value[1], &valStr); err != nil {
			continue
		}
		val, err := strconv.ParseFloat(valStr, 64)
		if err != nil || (val != val) { // skip NaN
			continue
		}
		results = append(results, Result{
			Metric: r.Metric,
			Value:  val,
			Time:   time.Unix(int64(ts), 0),
		})
	}
	return results, nil
}
