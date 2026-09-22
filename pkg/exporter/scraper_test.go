package exporter

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"xiaoshiai.cn/accelerator-exporter/pkg/accelerator"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestClientScrapeParsesPrometheusText(t *testing.T) {
	t.Parallel()
	client := NewHTTPScraper(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "http://dcgm:9400/metrics" {
			t.Fatalf("request URL = %q", request.URL.String())
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				"# TYPE DCGM_FI_DEV_GPU_UTIL gauge\n" +
					`DCGM_FI_DEV_GPU_UTIL{Hostname="node-a",UUID="GPU-1",modelName="A100"} 72` + "\n",
			)),
		}, nil
	})})
	families, err := client.Scrape(context.Background(), accelerator.Source{Name: "nvidia-dcgm-exporter", Address: "http://dcgm:9400/metrics"})
	if err != nil {
		t.Fatal(err)
	}
	samples := families["DCGM_FI_DEV_GPU_UTIL"]
	if len(samples) != 1 || samples[0].Value != 72 || samples[0].Labels["UUID"] != "GPU-1" {
		t.Fatalf("unexpected samples: %#v", samples)
	}
}

func TestClientScrapeRejectsBadStatusAndAddress(t *testing.T) {
	t.Parallel()
	client := NewHTTPScraper(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusBadGateway, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("bad gateway"))}, nil
	})})
	if _, err := client.Scrape(context.Background(), accelerator.Source{Name: "bad", Address: "not-a-url"}); err == nil {
		t.Fatal("expected invalid address error")
	}
	_, err := client.Scrape(context.Background(), accelerator.Source{Name: "nvidia-dcgm-exporter", Address: "http://dcgm/metrics"})
	if err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Fatalf("unexpected HTTP error: %v", err)
	}
	// The error must name the source configuration, not only the URL.
	if !strings.Contains(err.Error(), "nvidia-dcgm-exporter") {
		t.Fatalf("error does not name the source: %v", err)
	}
}
