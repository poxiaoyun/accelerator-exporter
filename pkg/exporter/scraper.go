package exporter

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"

	io_prometheus_client "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	prometheusmodel "github.com/prometheus/common/model"

	"xiaoshiai.cn/accelerator-exporter/pkg/accelerator"
)

const defaultMaxResponseBytes = 32 << 20

// HTTPScraper reads and parses the metrics of one HTTP endpoint.
type HTTPScraper struct {
	httpClient       *http.Client
	maxResponseBytes int64
}

// NewHTTPScraper creates the direct HTTP metrics reader.
func NewHTTPScraper(httpClient *http.Client) *HTTPScraper {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &HTTPScraper{httpClient: httpClient, maxResponseBytes: defaultMaxResponseBytes}
}

// Scrape fetches and parses one Prometheus text exposition endpoint.
//
// The whole target is taken rather than its address so every error names the
// source configuration the endpoint belongs to, which is what an operator needs
// when one of several configured sources misbehaves.
func (c *HTTPScraper) Scrape(ctx context.Context, target accelerator.Source) (accelerator.Families, error) {
	endpoint, err := url.Parse(target.Address)
	if err != nil {
		return nil, fmt.Errorf("parse metrics address of source %q: %w", target.Name, err)
	}
	if (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" {
		return nil, fmt.Errorf("metrics address of source %q must be an absolute HTTP(S) URL", target.Name)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create metrics request for source %q: %w", target.Name, err)
	}
	request.Header.Set("Accept", string(expfmt.FmtText))
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("request metrics from source %q: %w", target.Name, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, c.maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read metrics response of source %q: %w", target.Name, err)
	}
	if int64(len(body)) > c.maxResponseBytes {
		return nil, fmt.Errorf("metrics response of source %q exceeds %d bytes", target.Name, c.maxResponseBytes)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("source %q returned HTTP %d: %s", target.Name, response.StatusCode, string(body))
	}

	parser := expfmt.NewTextParser(prometheusmodel.LegacyValidation)
	metricFamilies, err := parser.TextToMetricFamilies(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("parse Prometheus metrics of source %q: %w", target.Name, err)
	}
	families := make(accelerator.Families, len(metricFamilies))
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
			families[name] = append(families[name], accelerator.RawSample{Labels: labels, Value: value})
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
