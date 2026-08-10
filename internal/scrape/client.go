// Package scrape fetches and parses Prometheus text exposition endpoints.
package scrape

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	prometheusmodel "github.com/prometheus/common/model"

	"xiaoshiai.cn/rune/internal/model"
)

const defaultMaxResponseBytes = 32 << 20

// Client directly scrapes accelerator exporter endpoints.
type Client struct {
	httpClient       *http.Client
	maxResponseBytes int64
}

// NewClient creates a metrics endpoint client.
func NewClient(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{httpClient: httpClient, maxResponseBytes: defaultMaxResponseBytes}
}

// Scrape fetches and parses one Prometheus text exposition endpoint.
func (c *Client) Scrape(ctx context.Context, address string) (model.Families, error) {
	endpoint, err := url.Parse(address)
	if err != nil {
		return nil, fmt.Errorf("parse metrics address: %w", err)
	}
	if (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" {
		return nil, fmt.Errorf("metrics address must be an absolute HTTP(S) URL")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create metrics request: %w", err)
	}
	request.Header.Set("Accept", string(expfmt.FmtText))
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("request metrics: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, c.maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read metrics response: %w", err)
	}
	if int64(len(body)) > c.maxResponseBytes {
		return nil, fmt.Errorf("metrics response exceeds %d bytes", c.maxResponseBytes)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("metrics endpoint returned HTTP %d: %s", response.StatusCode, string(body))
	}

	parser := expfmt.NewTextParser(prometheusmodel.LegacyValidation)
	metricFamilies, err := parser.TextToMetricFamilies(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("parse Prometheus metrics: %w", err)
	}
	families := make(model.Families, len(metricFamilies))
	for name, family := range metricFamilies {
		for _, metric := range family.Metric {
			value, ok := metricValue(family.GetType(), metric)
			if !ok {
				continue
			}
			labels := make(map[string]string, len(metric.Label))
			for _, pair := range metric.Label {
				labels[pair.GetName()] = pair.GetValue()
			}
			families[name] = append(families[name], model.RawSample{Labels: labels, Value: value})
		}
	}
	return families, nil
}

func metricValue(metricType io_prometheus_client.MetricType, metric *io_prometheus_client.Metric) (float64, bool) {
	switch metricType {
	case io_prometheus_client.MetricType_COUNTER:
		return metric.GetCounter().GetValue(), metric.Counter != nil
	case io_prometheus_client.MetricType_GAUGE:
		return metric.GetGauge().GetValue(), metric.Gauge != nil
	case io_prometheus_client.MetricType_UNTYPED:
		return metric.GetUntyped().GetValue(), metric.Untyped != nil
	default:
		return 0, false
	}
}
