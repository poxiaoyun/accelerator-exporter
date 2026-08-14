package exporter

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/common/expfmt"
	prometheusmodel "github.com/prometheus/common/model"

	"xiaoshiai.cn/rune/internal/model"
)

type fakeScraper struct {
	mu      sync.Mutex
	results map[string]model.Families
	errors  map[string]error
	seen    []string
}

func (f *fakeScraper) Scrape(_ context.Context, address string) (model.Families, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, address)
	return f.results[address], f.errors[address]
}

func TestRefreshScrapesMultipleAddressesAndAddsAcceleratorType(t *testing.T) {
	t.Parallel()
	scraper := &fakeScraper{results: map[string]model.Families{
		"http://gpu/metrics": {
			"DCGM_FI_DEV_GPU_UTIL": {{Labels: map[string]string{"Hostname": "node-gpu", "UUID": "GPU-1", "modelName": "A100"}, Value: 80}},
		},
		"http://npu/metrics": {
			"npu_chip_info_utilization": {{Labels: map[string]string{"id": "0", "model_name": "Ascend 910B"}, Value: 65}},
		},
	}, errors: map[string]error{}}
	metricExporter := newTestExporter(t, scraper, []model.Source{
		{Name: "gpu-node", Address: "http://gpu/metrics", AcceleratorType: model.AcceleratorTypeGPU},
		{Name: "npu-node", Address: "http://npu/metrics", AcceleratorType: model.AcceleratorTypeNPU, Labels: map[string]string{"node": "node-npu"}},
	})
	if err := metricExporter.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	body := metricsBody(metricExporter)
	parser := expfmt.NewTextParser(prometheusmodel.LegacyValidation)
	if _, err := parser.TextToMetricFamilies(strings.NewReader(body)); err != nil {
		t.Fatalf("exported metrics are invalid: %v\n%s", err, body)
	}
	for _, expected := range []string{
		`accelerator_core_utilization_percentage{accelerator_type="gpu",device="GPU-1",model="A100",node="node-gpu",uuid="GPU-1",vendor="nvidia"} 80`,
		`accelerator_core_utilization_percentage{accelerator_type="npu",device="0",model="Ascend 910B",node="node-npu",vendor="huawei"} 65`,
		`accelerator_exporter_target_up{accelerator_type="gpu",source="gpu-node"} 1`,
		`accelerator_exporter_target_up{accelerator_type="npu",source="npu-node"} 1`,
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("metrics do not contain %q:\n%s", expected, body)
		}
	}
	if len(scraper.seen) != 2 {
		t.Fatalf("scraped addresses = %#v", scraper.seen)
	}
	ready := httptest.NewRecorder()
	metricExporter.Ready(ready, httptest.NewRequest("GET", "/readyz", nil))
	if ready.Code != 200 {
		t.Fatalf("ready status = %d", ready.Code)
	}
}

func TestRefreshIsolatesFailedSource(t *testing.T) {
	t.Parallel()
	scraper := &fakeScraper{
		results: map[string]model.Families{"http://gpu/metrics": {
			"DCGM_FI_DEV_GPU_UTIL": {{Labels: map[string]string{"Hostname": "node-a", "device": "0", "modelName": "A100"}, Value: 42}},
		}},
		errors: map[string]error{"http://npu/metrics": errors.New("unavailable")},
	}
	metricExporter := newTestExporter(t, scraper, []model.Source{
		{Name: "gpu", Address: "http://gpu/metrics", AcceleratorType: model.AcceleratorTypeGPU},
		{Name: "npu", Address: "http://npu/metrics", AcceleratorType: model.AcceleratorTypeNPU},
	})
	if err := metricExporter.Refresh(context.Background()); err == nil {
		t.Fatal("expected partial refresh error")
	}
	body := metricsBody(metricExporter)
	for _, expected := range []string{
		`accelerator_exporter_last_refresh_success 0`,
		`accelerator_exporter_target_up{accelerator_type="gpu",source="gpu"} 1`,
		`accelerator_exporter_target_up{accelerator_type="npu",source="npu"} 0`,
		`accelerator_exporter_scrape_errors_total{accelerator_type="npu",source="npu"} 1`,
		`accelerator_core_utilization_percentage{accelerator_type="gpu"`,
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("metrics do not contain %q:\n%s", expected, body)
		}
	}
	parser := expfmt.NewTextParser(prometheusmodel.LegacyValidation)
	if _, err := parser.TextToMetricFamilies(strings.NewReader(body)); err != nil {
		t.Fatalf("invalid Prometheus exposition: %v\n%s", err, body)
	}
}

func TestRefreshDeduplicatesSameNormalizedSeriesByConfigOrder(t *testing.T) {
	t.Parallel()
	labels := map[string]string{"Hostname": "node-a", "device": "0", "modelName": "A100"}
	scraper := &fakeScraper{results: map[string]model.Families{
		"http://first/metrics":  {"DCGM_FI_DEV_GPU_UTIL": {{Labels: labels, Value: 10}}},
		"http://second/metrics": {"DCGM_FI_DEV_GPU_UTIL": {{Labels: labels, Value: 99}}},
	}, errors: map[string]error{}}
	metricExporter := newTestExporter(t, scraper, []model.Source{
		{Name: "first", Address: "http://first/metrics", AcceleratorType: model.AcceleratorTypeGPU},
		{Name: "second", Address: "http://second/metrics", AcceleratorType: model.AcceleratorTypeGPU},
	})
	if err := metricExporter.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	body := metricsBody(metricExporter)
	if !strings.Contains(body, `accelerator_core_utilization_percentage{accelerator_type="gpu",device="0",model="A100",node="node-a",vendor="nvidia"} 10`) {
		t.Fatalf("first configured source did not win:\n%s", body)
	}
	if !strings.Contains(body, "accelerator_exporter_duplicate_samples_total 3") {
		t.Fatalf("duplicate counter missing:\n%s", body)
	}
}

func TestMetricsUsesCanonicalCounterType(t *testing.T) {
	t.Parallel()
	labels := map[string]string{"Hostname": "node-a", "UUID": "GPU-1", "modelName": "A100", "xid": "31"}
	scraper := &fakeScraper{results: map[string]model.Families{
		"http://gpu/metrics": {
			"DCGM_EXP_XID_ERRORS_TOTAL": {{Labels: labels, Value: 2}},
		},
	}, errors: map[string]error{}}
	metricExporter := newTestExporter(t, scraper, []model.Source{{
		Name: "gpu", Address: "http://gpu/metrics", AcceleratorType: model.AcceleratorTypeGPU,
	}})
	if err := metricExporter.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	body := metricsBody(metricExporter)
	for _, expected := range []string{
		"# TYPE accelerator_error_total_count counter",
		`accelerator_error_total_count{accelerator_type="gpu",device="GPU-1",error_code="31",error_type="xid",model="A100",node="node-a",uuid="GPU-1",vendor="nvidia"} 2`,
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("metrics do not contain %q:\n%s", expected, body)
		}
	}
}

func TestNewRequiresProviderAndRefreshRejectsDuplicateNames(t *testing.T) {
	t.Parallel()
	options := NewDefaultOptions()
	if _, err := New(&fakeScraper{}, options, nil); err == nil {
		t.Fatal("expected missing provider error")
	}
	options.TargetProvider = fakeTargetProvider{targets: []model.Source{
		{Name: "same", Address: "http://a/metrics", AcceleratorType: model.AcceleratorTypeGPU},
		{Name: "same", Address: "http://b/metrics", AcceleratorType: model.AcceleratorTypeNPU},
	}}
	metricExporter, err := New(&fakeScraper{}, options, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := metricExporter.Refresh(context.Background()); err == nil {
		t.Fatal("expected duplicate discovered target error")
	}
}

type fakeTargetProvider struct {
	targets []model.Source
	err     error
}

func (provider fakeTargetProvider) Targets(context.Context) ([]model.Source, error) {
	return provider.targets, provider.err
}

func TestRefreshIsReadyWhenDynamicDiscoveryFindsNoTargets(t *testing.T) {
	t.Parallel()
	options := NewDefaultOptions()
	options.TargetProvider = fakeTargetProvider{}
	metricExporter, err := New(&fakeScraper{}, options, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := metricExporter.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	ready := httptest.NewRecorder()
	metricExporter.Ready(ready, httptest.NewRequest("GET", "/readyz", nil))
	if ready.Code != 200 {
		t.Fatalf("ready status = %d", ready.Code)
	}
	if !strings.Contains(metricsBody(metricExporter), "accelerator_exporter_targets_discovered 0") {
		t.Fatal("discovered target metric missing")
	}
}

func TestRefreshUsesPartialTargetsWhenDiscoveryPartiallyFails(t *testing.T) {
	t.Parallel()
	target := model.Source{Name: "gpu", Address: "http://gpu/metrics", AcceleratorType: model.AcceleratorTypeGPU}
	scraper := &fakeScraper{results: map[string]model.Families{
		target.Address: {"DCGM_FI_DEV_GPU_UTIL": {{Labels: map[string]string{"Hostname": "node-a", "device": "0", "modelName": "A100"}, Value: 50}}},
	}, errors: map[string]error{}}
	options := NewDefaultOptions()
	options.TargetProvider = fakeTargetProvider{targets: []model.Source{target}, err: errors.New("one namespace forbidden")}
	metricExporter, err := New(scraper, options, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := metricExporter.Refresh(context.Background()); err == nil {
		t.Fatal("expected discovery error")
	}
	body := metricsBody(metricExporter)
	if !strings.Contains(body, "accelerator_exporter_discovery_errors_total 1") || !strings.Contains(body, "accelerator_core_utilization_percentage{") {
		t.Fatalf("partial target was not retained:\n%s", body)
	}
}

func TestMetricsEscapesLabelValues(t *testing.T) {
	t.Parallel()
	labels := map[string]string{"device": `GPU-\"1`, "model": "A100\n80GB", "node": "node-a"}
	if got := formatLabels(labels); got != `{device="GPU-\\\"1",model="A100\n80GB",node="node-a"}` {
		t.Fatalf("formatLabels() = %q", got)
	}
}

func newTestExporter(t *testing.T, scraper Scraper, sources []model.Source) *Exporter {
	t.Helper()
	options := NewDefaultOptions()
	options.ScrapeInterval = time.Hour
	options.TargetProvider = fakeTargetProvider{targets: sources}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	metricExporter, err := New(scraper, options, logger)
	if err != nil {
		t.Fatal(err)
	}
	return metricExporter
}

func metricsBody(metricExporter *Exporter) string {
	response := httptest.NewRecorder()
	metricExporter.Metrics(response, httptest.NewRequest("GET", "/metrics", nil))
	return response.Body.String()
}
