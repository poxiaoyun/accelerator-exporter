package exporter

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/common/expfmt"
	prometheusmodel "github.com/prometheus/common/model"

	"xiaoshiai.cn/accelerator-exporter/pkg/accelerator"
)

type fakeScraper struct {
	mu      sync.Mutex
	results map[string]accelerator.Families
	errors  map[string]error
	seen    []string
}

func (f *fakeScraper) Scrape(_ context.Context, target accelerator.Source) (accelerator.Families, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, target.Name)
	return f.results[target.Address], f.errors[target.Address]
}

func TestRefreshScrapesMultipleAddressesAndResolvesIdentity(t *testing.T) {
	t.Parallel()
	scraper := &fakeScraper{results: map[string]accelerator.Families{
		"http://gpu/metrics": {
			"DCGM_FI_DEV_GPU_UTIL": {{Labels: map[string]string{"Hostname": "node-gpu", "UUID": "GPU-1", "modelName": "A100"}, Value: 80}},
		},
		"http://npu/metrics": {
			"npu_chip_info_utilization": {{Labels: map[string]string{"id": "0", "vdie_id": "Ascend-0", "model_name": "Ascend 910B"}, Value: 65}},
		},
	}, errors: map[string]error{}}
	metricExporter := newTestExporter(t, scraper, []accelerator.Source{
		{Name: "gpu-node", Address: "http://gpu/metrics", Rule: accelerator.RuleNvidiaDCGM},
		{Name: "npu-node", Address: "http://npu/metrics", Rule: accelerator.RuleAscendNPU, Labels: map[string]string{"node": "node-npu"}},
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
		`accelerator_core_utilization_percentage{device_id="GPU-1",device_mode="physical",device_type="gpu",model="A100",node="node-gpu",vendor="nvidia"} 80`,
		`accelerator_core_utilization_percentage{device_id="Ascend-0",device_mode="physical",device_type="npu",model="Ascend 910B",node="node-npu",vendor="huawei"} 65`,
		`accelerator_exporter_target_up{rule="nvidia-dcgm",source="gpu-node"} 1`,
		`accelerator_exporter_target_up{rule="ascend-npu",source="npu-node"} 1`,
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("metrics do not contain %q:\n%s", expected, body)
		}
	}
	if strings.Contains(body, "accelerator_type=") {
		t.Errorf("device layer must not carry accelerator_type:\n%s", body)
	}
	// The scraper sees the configured source name of each target, not only its
	// address. Scrapes run concurrently, so only the set is asserted.
	if len(scraper.seen) != 2 {
		t.Fatalf("scraped sources = %#v", scraper.seen)
	}
	sort.Strings(scraper.seen)
	if scraper.seen[0] != "gpu-node" || scraper.seen[1] != "npu-node" {
		t.Fatalf("scraped sources = %#v, want the configured source names", scraper.seen)
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
		results: map[string]accelerator.Families{"http://gpu/metrics": {
			"DCGM_FI_DEV_GPU_UTIL": {{Labels: map[string]string{"Hostname": "node-a", "device": "0", "modelName": "A100"}, Value: 42}},
		}},
		errors: map[string]error{"http://npu/metrics": errors.New("unavailable")},
	}
	metricExporter := newTestExporter(t, scraper, []accelerator.Source{
		{Name: "gpu", Address: "http://gpu/metrics", Rule: accelerator.RuleNvidiaDCGM},
		{Name: "npu", Address: "http://npu/metrics", Rule: accelerator.RuleAscendNPU},
	})
	if err := metricExporter.Refresh(context.Background()); err == nil {
		t.Fatal("expected partial refresh error")
	}
	body := metricsBody(metricExporter)
	for _, expected := range []string{
		`accelerator_exporter_last_refresh_success 0`,
		`accelerator_exporter_target_up{rule="nvidia-dcgm",source="gpu"} 1`,
		`accelerator_exporter_target_up{rule="ascend-npu",source="npu"} 0`,
		`accelerator_exporter_scrape_errors_total{rule="ascend-npu",source="npu"} 1`,
		`accelerator_core_utilization_percentage{device_id="0",`,
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

func TestRefreshReportsIdentityGapInsteadOfDropping(t *testing.T) {
	t.Parallel()
	// The source names neither a parent device nor a usable device_id. The
	// measurement is still reported, and the gap is counted.
	scraper := &fakeScraper{results: map[string]accelerator.Families{
		"http://npu/metrics": {
			"vnpu_pod_aicore_utilization": {{Labels: map[string]string{"v_dev_id": "100", "model_name": "Ascend 910B"}, Value: 33}},
		},
	}, errors: map[string]error{}}
	metricExporter := newTestExporter(t, scraper, []accelerator.Source{
		{Name: "vnpu", Address: "http://npu/metrics", Rule: accelerator.RuleAscendNPU, Labels: map[string]string{"node": "node-npu"}},
	})
	if err := metricExporter.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	body := metricsBody(metricExporter)
	if !strings.Contains(body, `accelerator_core_utilization_percentage{`) ||
		!strings.Contains(body, `device_id="100"`) {
		t.Errorf("an identified target without a parent must still be reported:\n%s", body)
	}
	for _, expected := range []string{
		// One core utilization series and one inventory series, each breaching
		// both the decimal device_id rule and the missing parent rule.
		`accelerator_exporter_contract_violations_total{reason="decimal_device_id"} 2`,
		`accelerator_exporter_contract_violations_total{reason="missing_parent_device_id"} 2`,
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("metrics do not contain %q:\n%s", expected, body)
		}
	}
}

func TestRefreshDropsPhysicalOnlyMetricsOnInstances(t *testing.T) {
	t.Parallel()
	labels := map[string]string{
		"Hostname": "node-gpu", "UUID": "GPU-1", "modelName": "A100",
		"GPU_I_ID": "3", "GPU_I_PROFILE": "1g.10gb",
	}
	scraper := &fakeScraper{results: map[string]accelerator.Families{
		"http://gpu/metrics": {
			"DCGM_FI_DEV_GPU_UTIL":    {{Labels: labels, Value: 40}},
			"DCGM_FI_DEV_GPU_TEMP":    {{Labels: labels, Value: 58}},
			"DCGM_FI_DEV_POWER_USAGE": {{Labels: labels, Value: 90}},
		},
	}, errors: map[string]error{}}
	metricExporter := newTestExporter(t, scraper, []accelerator.Source{
		{Name: "gpu", Address: "http://gpu/metrics", Rule: accelerator.RuleNvidiaDCGM},
	})
	if err := metricExporter.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	body := metricsBody(metricExporter)
	if !strings.Contains(body, `accelerator_core_utilization_percentage{device_id="GPU-1.3",device_index="3",device_mode="partition",device_type="gpu",model="A100",node="node-gpu",parent_device_id="GPU-1",vendor="nvidia"} 40`) {
		t.Errorf("partition utilization missing:\n%s", body)
	}
	for _, forbidden := range []string{"accelerator_temperature_celsius", "accelerator_power_usage_watts"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("%s must not be copied onto a partition:\n%s", forbidden, body)
		}
	}
	// Not producing the series is the contract's intent, not a breach, so it is
	// not counted as a violation.
	if strings.Contains(body, `reason="inapplicable_mode"`) {
		t.Errorf("a partition without independent readings is expected, not a violation:\n%s", body)
	}
}

func TestRefreshPublishesStaticInformationOnlyOnInventory(t *testing.T) {
	t.Parallel()
	labels := map[string]string{
		"Hostname": "node-gpu", "UUID": "GPU-1", "modelName": "A100",
		"GPU_I_ID": "3", "GPU_I_PROFILE": "1g.10gb",
		"DCGM_FI_DRIVER_VERSION": "550.90.07", "pci_bus_id": "00000000:07:00.0",
	}
	scraper := &fakeScraper{results: map[string]accelerator.Families{
		"http://gpu/metrics": {"DCGM_FI_DEV_GPU_UTIL": {{Labels: labels, Value: 40}}},
	}, errors: map[string]error{}}
	metricExporter := newTestExporter(t, scraper, []accelerator.Source{
		{Name: "gpu", Address: "http://gpu/metrics", Rule: accelerator.RuleNvidiaDCGM},
	})
	if err := metricExporter.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	body := metricsBody(metricExporter)
	if !strings.Contains(body, `accelerator_info{device_id="GPU-1.3",device_index="3",device_mode="partition",device_type="gpu",driver_version="550.90.07",model="A100",node="node-gpu",parent_device_id="GPU-1",pci_bus_id="00000000:07:00.0",profile="1g.10gb",vendor="nvidia"} 1`) {
		t.Errorf("inventory must carry the static information:\n%s", body)
	}
	if !strings.Contains(body, `accelerator_core_utilization_percentage{device_id="GPU-1.3",device_index="3",device_mode="partition",device_type="gpu",model="A100",node="node-gpu",parent_device_id="GPU-1",vendor="nvidia"} 40`) {
		t.Errorf("utilization must not carry static information:\n%s", body)
	}
}

func TestRefreshJoinsAllocationFactsIntoTheRelationLayer(t *testing.T) {
	t.Parallel()
	labels := map[string]string{"Hostname": "node-gpu", "UUID": "GPU-1", "modelName": "A100"}
	scraper := &fakeScraper{results: map[string]accelerator.Families{
		"http://gpu/metrics": {
			"DCGM_FI_DEV_GPU_UTIL": {{Labels: labels, Value: 80}},
			// A vendor workload label must not become ownership.
			"DCGM_FI_DEV_FB_USED": {{Labels: withLabel(labels, "pod", "vendor-label"), Value: 1}},
		},
	}, errors: map[string]error{}}
	allocations := fakeAllocationProvider{allocations: []accelerator.Allocation{
		{Namespace: "train", Pod: "job-0", PodUID: "uid-1", Container: "main", NativeResource: "nvidia.com/gpu", NativeDeviceID: "GPU-1"},
		// Resolves to no observation target and must not produce a series.
		{Namespace: "train", Pod: "job-1", PodUID: "uid-2", Container: "main", NativeResource: "nvidia.com/gpu", NativeDeviceID: "GPU-9"},
	}}
	targets := fakeTargetProvider{targets: []accelerator.Source{
		{Name: "gpu", Address: "http://gpu/metrics", Rule: accelerator.RuleNvidiaDCGM},
	}}
	metricExporter, err := New(NewDefaultOptions(), scraper, targets, allocations, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	if err := metricExporter.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	body := metricsBody(metricExporter)
	if !strings.Contains(body, `accelerator_pod_info{container="main",device_id="GPU-1",namespace="train",node="node-gpu",pod="job-0",pod_uid="uid-1"} 1`) {
		t.Fatalf("relation layer missing:\n%s", body)
	}
	if strings.Contains(body, "job-1") {
		t.Errorf("an unresolved allocation must not produce a series:\n%s", body)
	}
	if strings.Contains(body, `pod="vendor-label"`) {
		t.Errorf("a vendor workload label must not become ownership:\n%s", body)
	}
}

func TestRefreshDeduplicatesSameNormalizedSeriesByConfigOrder(t *testing.T) {
	t.Parallel()
	labels := map[string]string{"Hostname": "node-a", "device": "0", "modelName": "A100"}
	scraper := &fakeScraper{results: map[string]accelerator.Families{
		"http://first/metrics":  {"DCGM_FI_DEV_GPU_UTIL": {{Labels: labels, Value: 10}}},
		"http://second/metrics": {"DCGM_FI_DEV_GPU_UTIL": {{Labels: labels, Value: 99}}},
	}, errors: map[string]error{}}
	metricExporter := newTestExporter(t, scraper, []accelerator.Source{
		{Name: "first", Address: "http://first/metrics", Rule: accelerator.RuleNvidiaDCGM},
		{Name: "second", Address: "http://second/metrics", Rule: accelerator.RuleNvidiaDCGM},
	})
	if err := metricExporter.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	body := metricsBody(metricExporter)
	if !strings.Contains(body, `accelerator_core_utilization_percentage{device_id="0",device_mode="physical",device_type="gpu",model="A100",node="node-a",vendor="nvidia"} 10`) {
		t.Fatalf("first configured source did not win:\n%s", body)
	}
	if !strings.Contains(body, "accelerator_exporter_duplicate_samples_total 2") {
		t.Fatalf("duplicate counter missing:\n%s", body)
	}
}

// TestRefreshPrefersTheFirstConfiguredSourceForASharedTarget is the case the
// configuration order exists for: DCGM reads a card from the driver and the vGPU
// monitor reads the same card from its own sampler, so both map one metric onto
// one observation target. Which reading survives is decided by which source is
// configured first, not by which name sorts first and not by which scrape
// happened to finish first.
func TestRefreshPrefersTheFirstConfiguredSourceForASharedTarget(t *testing.T) {
	t.Parallel()
	// Both sources describe the same card and resolve to the same label set.
	dcgm := accelerator.Source{
		Name: "z-dcgm", Address: "http://dcgm/metrics", Rule: accelerator.RuleNvidiaDCGM,
	}
	vGPU := accelerator.Source{
		Name: "a-vgpu-monitor", Address: "http://vgpu/metrics", Rule: accelerator.RuleVolcanoVGPU,
		Labels: map[string]string{"node": "node-a", "model": "A100"},
	}
	card := "GPU-1"
	for _, test := range []struct {
		name    string
		sources []accelerator.Source
		want    string
	}{
		{
			name:    "the vGPU monitor is configured first",
			sources: []accelerator.Source{vGPU, dcgm},
			want:    `accelerator_core_utilization_percentage{device_id="GPU-1",device_mode="physical",device_type="gpu",model="A100",node="node-a",vendor="nvidia"} 7`,
		},
		{
			name:    "DCGM is configured first",
			sources: []accelerator.Source{dcgm, vGPU},
			want:    `accelerator_core_utilization_percentage{device_id="GPU-1",device_mode="physical",device_type="gpu",model="A100",node="node-a",vendor="nvidia"} 80`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			scraper := &fakeScraper{
				results: map[string]accelerator.Families{
					"http://dcgm/metrics": {"DCGM_FI_DEV_GPU_UTIL": {{Labels: map[string]string{
						"Hostname": "node-a", "UUID": card, "modelName": "A100",
					}, Value: 80}}},
					"http://vgpu/metrics": {"HostCoreUtilization": {{Labels: map[string]string{
						"deviceuuid": card, "deviceidx": "0", "zone": "vGPU",
					}, Value: 7}}},
				},
				errors: map[string]error{},
			}
			metricExporter := newTestExporter(t, scraper, test.sources)
			if err := metricExporter.Refresh(context.Background()); err != nil {
				t.Fatal(err)
			}
			body := metricsBody(metricExporter)
			if !strings.Contains(body, test.want) {
				t.Fatalf("want %q in:\n%s", test.want, body)
			}
			// Two series collide: the utilization both sources map onto the card,
			// and accelerator_info, which every rule derives per target.
			if !strings.Contains(body, "accelerator_exporter_duplicate_samples_total 2") {
				t.Fatalf("the losing reading must be counted as a duplicate:\n%s", body)
			}
		})
	}
}

func TestRefreshDeduplicatesTheInventoryByObservationTarget(t *testing.T) {
	t.Parallel()
	// The two sources describe the same card, but only DCGM reads the driver
	// version and the PCI position, so their inventories do not share a label
	// set. The inventory is keyed by the observation target, not by the label
	// set, so the card is listed once and the losing source is counted as a
	// duplicate.
	dcgm := accelerator.Source{
		Name: "dcgm", Address: "http://dcgm/metrics", Rule: accelerator.RuleNvidiaDCGM,
	}
	vGPU := accelerator.Source{
		Name: "vgpu", Address: "http://vgpu/metrics", Rule: accelerator.RuleVolcanoVGPU,
		Labels: map[string]string{"node": "node-a", "model": "A100"},
	}
	card := "GPU-1"
	scraper := &fakeScraper{
		results: map[string]accelerator.Families{
			"http://dcgm/metrics": {"DCGM_FI_DEV_GPU_UTIL": {{Labels: map[string]string{
				"Hostname": "node-a", "UUID": card, "modelName": "A100",
				"driver_version": "550.90.07", "pci_bus_id": "00000000:07:00.0",
			}, Value: 80}}},
			"http://vgpu/metrics": {"HostCoreUtilization": {{Labels: map[string]string{
				"deviceuuid": card, "deviceidx": "0", "zone": "vGPU",
			}, Value: 7}}},
		},
		errors: map[string]error{},
	}
	for _, test := range []struct {
		name    string
		sources []accelerator.Source
		want    string
	}{
		{
			name:    "DCGM is configured first",
			sources: []accelerator.Source{dcgm, vGPU},
			want:    `accelerator_info{device_id="GPU-1",device_mode="physical",device_type="gpu",driver_version="550.90.07",model="A100",node="node-a",pci_bus_id="00000000:07:00.0",vendor="nvidia"} 1`,
		},
		{
			// The inventory of the first source survives, and this one carries no
			// static information because the vGPU monitor does not report any.
			name:    "the vGPU monitor is configured first",
			sources: []accelerator.Source{vGPU, dcgm},
			want:    `accelerator_info{device_id="GPU-1",device_mode="physical",device_type="gpu",model="A100",node="node-a",vendor="nvidia"} 1`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			metricExporter := newTestExporter(t, scraper, test.sources)
			if err := metricExporter.Refresh(context.Background()); err != nil {
				t.Fatal(err)
			}
			body := metricsBody(metricExporter)
			parser := expfmt.NewTextParser(prometheusmodel.LegacyValidation)
			families, err := parser.TextToMetricFamilies(strings.NewReader(body))
			if err != nil {
				t.Fatalf("invalid Prometheus exposition: %v\n%s", err, body)
			}
			if got := len(families["accelerator_info"].GetMetric()); got != 1 {
				t.Fatalf("the inventory must list the observation target once, got %d:\n%s", got, body)
			}
			if !strings.Contains(body, test.want) {
				t.Fatalf("want %q in:\n%s", test.want, body)
			}
			// The card's utilization and the losing inventory each collide once.
			if !strings.Contains(body, "accelerator_exporter_duplicate_samples_total 2") {
				t.Fatalf("the losing readings must be counted as duplicates:\n%s", body)
			}
		})
	}
}

func TestMetricsUsesCanonicalCounterType(t *testing.T) {
	t.Parallel()
	labels := map[string]string{"Hostname": "node-a", "UUID": "GPU-1", "modelName": "A100", "xid": "31"}
	scraper := &fakeScraper{results: map[string]accelerator.Families{
		"http://gpu/metrics": {
			"DCGM_EXP_XID_ERRORS_TOTAL": {{Labels: labels, Value: 2}},
		},
	}, errors: map[string]error{}}
	metricExporter := newTestExporter(t, scraper, []accelerator.Source{{
		Name: "gpu", Address: "http://gpu/metrics", Rule: accelerator.RuleNvidiaDCGM,
	}})
	if err := metricExporter.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	body := metricsBody(metricExporter)
	for _, expected := range []string{
		"# TYPE accelerator_errors_total counter",
		`accelerator_errors_total{device_id="GPU-1",device_mode="physical",device_type="gpu",error_code="31",error_type="xid",model="A100",node="node-a",vendor="nvidia"} 2`,
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("metrics do not contain %q:\n%s", expected, body)
		}
	}
}

func TestRefreshFansHealthDiagnosticsIntoOneState(t *testing.T) {
	t.Parallel()
	labels := map[string]string{"Hostname": "node-a", "UUID": "GPU-1", "modelName": "A100"}
	healthLabels := withLabel(labels, "health_error_severity", "ISOLATE")
	healthLabels["health_error_category"] = "HARDWARE_MEMORY"
	healthLabels["health_error_code"] = "DCGM_FR_VOLATILE_DBE_DETECTED"
	scraper := &fakeScraper{results: map[string]accelerator.Families{
		"http://gpu/metrics": {
			"DCGM_FI_DEV_XID_ERRORS":     {{Labels: labels, Value: 0}},
			"DCGM_EXP_GPU_HEALTH_STATUS": {{Labels: healthLabels, Value: 20}},
		},
	}, errors: map[string]error{}}
	metricExporter := newTestExporter(t, scraper, []accelerator.Source{
		{Name: "gpu", Address: "http://gpu/metrics", Rule: accelerator.RuleNvidiaDCGM},
	})
	if err := metricExporter.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	body := metricsBody(metricExporter)
	if !strings.Contains(body, `accelerator_health_state{device_id="GPU-1",device_mode="physical",device_type="gpu",model="A100",node="node-a",state="fault",vendor="nvidia"} 1`) {
		t.Errorf("health state missing:\n%s", body)
	}
	if !strings.Contains(body, `accelerator_health_diagnostic_info{category="hardware_memory",code="dcgm_fr_volatile_dbe_detected",device_id="GPU-1",device_mode="physical",device_type="gpu",model="A100",node="node-a",severity="critical",vendor="nvidia"} 1`) {
		t.Errorf("health diagnostic missing:\n%s", body)
	}
	if strings.Contains(body, "accelerator_health_status") {
		t.Errorf("the old health metric must be gone:\n%s", body)
	}
}

// TestRefreshReportsTheAllocationSourceOutcome covers the failure and recovery
// of the device allocation source. A node whose allocation source stopped
// answering and a node that has none both leave the relation layer empty, and
// the two must be distinguishable from the metrics alone.
func TestRefreshReportsTheAllocationSourceOutcome(t *testing.T) {
	t.Parallel()
	scraper := &fakeScraper{results: map[string]accelerator.Families{
		"http://gpu/metrics": {
			"DCGM_FI_DEV_GPU_UTIL": {{Labels: map[string]string{"Hostname": "node-a", "UUID": "GPU-1"}, Value: 80}},
		},
	}, errors: map[string]error{}}
	allocationSource := &fakeAllocationProvider{err: errors.New("kubelet is unreachable")}
	targets := fakeTargetProvider{targets: []accelerator.Source{
		{Name: "gpu", Address: "http://gpu/metrics", Rule: accelerator.RuleNvidiaDCGM},
	}}
	metricExporter, err := New(NewDefaultOptions(), scraper, targets, allocationSource, testLogger())
	if err != nil {
		t.Fatal(err)
	}

	if err := metricExporter.Refresh(context.Background()); err == nil {
		t.Fatal("a failed allocation read must be reported")
	}
	body := metricsBody(metricExporter)
	for _, expected := range []string{
		"accelerator_exporter_allocation_source_configured 1",
		"accelerator_exporter_allocation_source_up 0",
		"accelerator_exporter_allocation_errors_total 1",
		"accelerator_exporter_last_refresh_success 0",
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("metrics do not contain %q:\n%s", expected, body)
		}
	}

	// The source recovers: the gauge follows it, the counter stays cumulative, and
	// the device layer was produced throughout.
	allocationSource.err = nil
	if err := metricExporter.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	body = metricsBody(metricExporter)
	for _, expected := range []string{
		"accelerator_exporter_allocation_source_up 1",
		"accelerator_exporter_allocation_errors_total 1",
		"accelerator_exporter_last_refresh_success 1",
		"accelerator_core_utilization_percentage{",
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("metrics do not contain %q:\n%s", expected, body)
		}
	}
}

func TestNewRequiresItsSourcesAndOptions(t *testing.T) {
	t.Parallel()
	targets := fakeTargetProvider{}
	options := NewDefaultOptions()
	if _, err := New(options, &fakeScraper{}, nil, nil, testLogger()); err == nil {
		t.Fatal("expected missing target provider error")
	}
	if _, err := New(options, nil, targets, nil, testLogger()); err == nil {
		t.Fatal("expected missing scraper error")
	}
	if _, err := New(nil, &fakeScraper{}, targets, nil, testLogger()); err == nil {
		t.Fatal("expected missing options error")
	}
	// No allocation provider is a valid configuration: the node simply has no
	// device allocation source.
	if _, err := New(options, &fakeScraper{}, targets, nil, testLogger()); err != nil {
		t.Fatalf("a node without an allocation source must still start: %v", err)
	}
}

func TestNewRejectsInvalidOptions(t *testing.T) {
	t.Parallel()
	tests := map[string]*Options{
		"no interval": {ScrapeTimeout: time.Second, MaxConcurrency: 1},
		"no timeout":  {ScrapeInterval: time.Second, MaxConcurrency: 1},
		"no workers":  {ScrapeInterval: time.Second, ScrapeTimeout: time.Second},
	}
	for name, options := range tests {
		if _, err := New(options, &fakeScraper{}, fakeTargetProvider{}, nil, testLogger()); err == nil {
			t.Errorf("%s: expected an options error", name)
		}
	}
}

func TestRefreshRejectsDuplicateSourceNames(t *testing.T) {
	t.Parallel()
	scraper := &fakeScraper{results: map[string]accelerator.Families{}, errors: map[string]error{}}
	targets := fakeTargetProvider{targets: []accelerator.Source{
		{Name: "same", Address: "http://a/metrics", Rule: accelerator.RuleNvidiaDCGM},
		{Name: "same", Address: "http://b/metrics", Rule: accelerator.RuleAscendNPU},
	}}
	metricExporter, err := New(NewDefaultOptions(), scraper, targets, nil, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	if err := metricExporter.Refresh(context.Background()); err == nil {
		t.Fatal("expected duplicate discovered target error")
	}
}

func TestRefreshRejectsUnknownRule(t *testing.T) {
	t.Parallel()
	targets := fakeTargetProvider{targets: []accelerator.Source{
		{Name: "custom", Address: "http://a/metrics", Rule: "not-a-rule"},
	}}
	metricExporter, err := New(NewDefaultOptions(), &fakeScraper{}, targets, nil, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	if err := metricExporter.Refresh(context.Background()); err == nil {
		t.Fatal("expected unknown rule error")
	}
	if !strings.Contains(metricsBody(metricExporter), "accelerator_exporter_discovery_errors_total 1") {
		t.Fatal("unknown rule must be reported as a discovery error")
	}
}

type fakeTargetProvider struct {
	targets []accelerator.Source
	err     error
}

func (provider fakeTargetProvider) Targets(context.Context) ([]accelerator.Source, error) {
	return provider.targets, provider.err
}

type fakeAllocationProvider struct {
	allocations []accelerator.Allocation
	err         error
}

func (provider fakeAllocationProvider) Allocations(context.Context) ([]accelerator.Allocation, error) {
	return provider.allocations, provider.err
}

func TestRefreshIsReadyWhenDynamicDiscoveryFindsNoTargets(t *testing.T) {
	t.Parallel()
	metricExporter, err := New(NewDefaultOptions(), &fakeScraper{}, fakeTargetProvider{}, nil, testLogger())
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
	target := accelerator.Source{Name: "gpu", Address: "http://gpu/metrics", Rule: accelerator.RuleNvidiaDCGM}
	scraper := &fakeScraper{results: map[string]accelerator.Families{
		target.Address: {"DCGM_FI_DEV_GPU_UTIL": {{Labels: map[string]string{"Hostname": "node-a", "device": "0", "modelName": "A100"}, Value: 50}}},
	}, errors: map[string]error{}}
	targets := fakeTargetProvider{targets: []accelerator.Source{target}, err: errors.New("one namespace forbidden")}
	metricExporter, err := New(NewDefaultOptions(), scraper, targets, nil, testLogger())
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
	labels := map[string]string{"device_id": `GPU-\"1`, "model": "A100\n80GB", "node": "node-a"}
	if got := formatLabels(labels); got != `{device_id="GPU-\\\"1",model="A100\n80GB",node="node-a"}` {
		t.Fatalf("formatLabels() = %q", got)
	}
}

func newTestExporter(t *testing.T, scraper Scraper, targets []accelerator.Source) *Exporter {
	t.Helper()
	options := NewDefaultOptions()
	options.ScrapeInterval = time.Hour
	metricExporter, err := New(options, scraper, fakeTargetProvider{targets: targets}, nil, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	return metricExporter
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func metricsBody(metricExporter *Exporter) string {
	response := httptest.NewRecorder()
	metricExporter.Metrics(response, httptest.NewRequest("GET", "/metrics", nil))
	return response.Body.String()
}

func withLabel(labels map[string]string, name, value string) map[string]string {
	result := make(map[string]string, len(labels)+1)
	for key, existing := range labels {
		result[key] = existing
	}
	result[name] = value
	return result
}
