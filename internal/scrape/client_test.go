package scrape

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestClientScrapeParsesPrometheusText(t *testing.T) {
	t.Parallel()
	client := NewClient(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
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
	families, err := client.Scrape(context.Background(), "http://dcgm:9400/metrics")
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
	client := NewClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusBadGateway, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("bad gateway"))}, nil
	})})
	if _, err := client.Scrape(context.Background(), "not-a-url"); err == nil {
		t.Fatal("expected invalid address error")
	}
	if _, err := client.Scrape(context.Background(), "http://dcgm/metrics"); err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Fatalf("unexpected HTTP error: %v", err)
	}
}
