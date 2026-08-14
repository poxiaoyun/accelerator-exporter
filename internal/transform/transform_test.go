package transform

import (
	"math"
	"testing"

	"xiaoshiai.cn/rune/internal/model"
)

func TestTransformGPUAndDerivedMemoryMetrics(t *testing.T) {
	t.Parallel()
	source := model.Source{Name: "gpu-a", AcceleratorType: model.AcceleratorTypeGPU}
	labels := map[string]string{"Hostname": "node-a", "UUID": "GPU-1", "modelName": "A100", "namespace": "train", "pod": "job-0"}
	families := model.Families{
		"DCGM_FI_DEV_GPU_UTIL":      {{Labels: labels, Value: 75}},
		"DCGM_FI_DEV_MEM_COPY_UTIL": {{Labels: labels, Value: 30}},
		"DCGM_FI_DEV_FB_USED":       {{Labels: labels, Value: 2}},
		"DCGM_FI_DEV_FB_FREE":       {{Labels: labels, Value: 6}},
	}
	samples, dropped := Transform(source, families, nil)
	if dropped != 0 {
		t.Fatalf("dropped = %d", dropped)
	}
	assertSample(t, samples, "accelerator_core_utilization_percentage", "gpu", 75)
	assertSample(t, samples, "accelerator_memory_bandwidth_utilization_percentage", "gpu", 30)
	assertSample(t, samples, "accelerator_memory_used_bytes", "gpu", 2*bytesPerMiB)
	assertSample(t, samples, "accelerator_memory_total_bytes", "gpu", 8*bytesPerMiB)
	assertSample(t, samples, "accelerator_memory_utilization_percentage", "gpu", 25)
	assertSample(t, samples, "accelerator_info", "gpu", 1)
	assertSample(t, samples, "accelerator_total_count", "gpu", 1)
	assertSample(t, samples, "accelerator_used_count", "gpu", 1)
}

func TestCanonicalMetricContractContainsCloudAcceleratorMetrics(t *testing.T) {
	t.Parallel()
	want := []string{
		"accelerator_batch_size_count",
		"accelerator_core_utilization_percentage",
		"accelerator_error_total_count",
		"accelerator_health_status",
		"accelerator_info",
		"accelerator_io_utilization_percentage",
		"accelerator_latency_seconds",
		"accelerator_memory_bandwidth_utilization_percentage",
		"accelerator_memory_free_bytes",
		"accelerator_memory_oom_total_count",
		"accelerator_memory_total_bytes",
		"accelerator_memory_used_bytes",
		"accelerator_memory_utilization_percentage",
		"accelerator_operations_per_second",
		"accelerator_operations_total_count",
		"accelerator_overall_utilization_percentage",
		"accelerator_partition_total_count",
		"accelerator_power_limit_watts",
		"accelerator_power_usage_watts",
		"accelerator_queue_depth_count",
		"accelerator_share_percentage",
		"accelerator_temperature_celsius",
		"accelerator_throttle_reason_info",
		"accelerator_total_count",
		"accelerator_used_count",
	}
	got := CanonicalMetricNames()
	if len(got) != len(want) {
		t.Fatalf("canonical metric count = %d, want %d: %v", len(got), len(want), got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("canonical metrics = %v, want %v", got, want)
		}
	}
	for _, name := range []string{"accelerator_error_total_count", "accelerator_memory_oom_total_count", "accelerator_operations_total_count"} {
		metadata, ok := CanonicalMetric(name)
		if !ok || metadata.Type != MetricTypeCounter {
			t.Errorf("%s metadata = %#v, %v", name, metadata, ok)
		}
	}
}

func TestTransformPassesThroughCanonicalMetrics(t *testing.T) {
	t.Parallel()
	source := model.Source{Name: "custom", AcceleratorType: model.AcceleratorTypeGPU}
	labels := map[string]string{
		"Hostname": "node-a", "UUID": "GPU-1", "modelName": "A100",
		"pod_namespace": "train", "pod_name": "job-0", "container_name": "main", "quantile": "0.99",
	}
	samples, dropped := Transform(source, model.Families{
		"accelerator_latency_seconds": {{Labels: labels, Value: 0.125}},
	}, nil)
	if dropped != 0 {
		t.Fatalf("dropped = %d", dropped)
	}
	sample := findSample(t, samples, "accelerator_latency_seconds")
	if sample.Value != 0.125 || sample.Labels["quantile"] != "0.99" || sample.Labels["accelerator_type"] != "gpu" ||
		sample.Labels["node"] != "node-a" || sample.Labels["device"] != "GPU-1" || sample.Labels["model"] != "A100" ||
		sample.Labels["namespace"] != "train" || sample.Labels["pod"] != "job-0" || sample.Labels["container"] != "main" {
		t.Fatalf("unexpected canonical sample: %#v", sample)
	}
	for _, alias := range []string{"Hostname", "UUID", "modelName", "pod_namespace", "pod_name", "container_name"} {
		if _, ok := sample.Labels[alias]; ok {
			t.Errorf("identity alias %q was not normalized: %#v", alias, sample)
		}
	}
}

func TestTransformGPUHealthErrorsAndThrottleReason(t *testing.T) {
	t.Parallel()
	source := model.Source{Name: "gpu", AcceleratorType: model.AcceleratorTypeGPU}
	labels := map[string]string{"Hostname": "node-a", "UUID": "GPU-1", "modelName": "A100"}
	samples, dropped := Transform(source, model.Families{
		"DCGM_EXP_GPU_HEALTH_STATUS": {{Labels: withLabels(labels, map[string]string{
			"health_watch": "MEM", "health_error_code": "DCGM_FR_VOLATILE_DBE_DETECTED",
			"health_error_severity": "ISOLATE", "health_error_category": "HARDWARE_MEMORY",
		}), Value: 20}},
		"DCGM_FI_DEV_XID_ERRORS":          {{Labels: labels, Value: 0}},
		"DCGM_FI_DEV_ROW_REMAP_FAILURE":   {{Labels: labels, Value: 1}},
		"DCGM_EXP_XID_ERRORS_TOTAL":       {{Labels: withLabel(labels, "xid", "31"), Value: 3}},
		"DCGM_EXP_CLOCK_EVENTS_COUNT":     {{Labels: withLabel(labels, "clock_event", "thermal"), Value: 2}},
		"DCGM_FI_DEV_PCIE_REPLAY_COUNTER": {{Labels: labels, Value: 4}},
	}, nil)
	if dropped != 0 {
		t.Fatalf("dropped = %d", dropped)
	}
	assertSample(t, samples, "accelerator_health_status", "gpu", 1)
	health := findSampleWithLabel(t, samples, "accelerator_health_status", "health_watch", "MEM")
	if health.Value != 0 || health.Labels["health_error_severity"] != "ISOLATE" || health.Labels["health_error_category"] != "HARDWARE_MEMORY" {
		t.Fatalf("unexpected DCGM health sample: %#v", health)
	}
	rowRemap := findSampleWithLabel(t, samples, "accelerator_health_status", "health_watch", "ROW_REMAP")
	if rowRemap.Value != 0 {
		t.Fatalf("unexpected row remap health sample: %#v", rowRemap)
	}
	xid := findSampleWithLabel(t, samples, "accelerator_error_total_count", "error_type", "xid")
	if xid.Value != 3 || xid.Labels["error_code"] != "31" {
		t.Fatalf("unexpected XID error sample: %#v", xid)
	}
	pcie := findSampleWithLabel(t, samples, "accelerator_error_total_count", "error_type", "pcie_replay")
	if pcie.Value != 4 {
		t.Fatalf("unexpected PCIe error sample: %#v", pcie)
	}
	throttle := findSampleWithLabel(t, samples, "accelerator_throttle_reason_info", "reason", "thermal")
	if throttle.Value != 1 {
		t.Fatalf("unexpected throttle sample: %#v", throttle)
	}
}

func TestTransformGPUHealthStatusFromXID(t *testing.T) {
	t.Parallel()
	source := model.Source{Name: "gpu", AcceleratorType: model.AcceleratorTypeGPU}
	labels := map[string]string{"Hostname": "node-a", "UUID": "GPU-1", "modelName": "A100"}
	samples, _ := Transform(source, model.Families{
		"DCGM_FI_DEV_XID_ERRORS": {{Labels: labels, Value: 13}},
	}, nil)
	assertSample(t, samples, "accelerator_health_status", "gpu", 0)
}

func TestTransformNormalizesGPUPartition(t *testing.T) {
	t.Parallel()
	source := model.Source{Name: "gpu", AcceleratorType: model.AcceleratorTypeGPU}
	labels := map[string]string{
		"Hostname": "node-a", "UUID": "MIG-GPU-1/3/0", "GPU_I_ID": "3", "modelName": "A100",
	}
	samples, dropped := Transform(source, model.Families{
		"DCGM_FI_DEV_GPU_UTIL": {{Labels: labels, Value: 40}},
	}, nil)
	if dropped != 0 {
		t.Fatalf("dropped = %d", dropped)
	}
	partition := findSample(t, samples, "accelerator_partition_total_count")
	if partition.Value != 1 || partition.Labels["partition"] != "3" {
		t.Fatalf("unexpected GPU partition sample: %#v", partition)
	}
}

func TestTransformNPUHealthUsageAndErrors(t *testing.T) {
	t.Parallel()
	source := model.Source{
		Name: "npu", AcceleratorType: model.AcceleratorTypeNPU,
		Labels: map[string]string{"node": "node-npu"},
	}
	labels := map[string]string{
		"id": "0", "vdie_id": "Ascend-0", "model_name": "Ascend 910B",
		"namespace": "train", "pod_name": "job-0", "container_name": "main",
	}
	samples, dropped := Transform(source, model.Families{
		"npu_chip_info_health_status":                      {{Labels: labels, Value: 1}},
		"npu_chip_info_process_info_num":                   {{Labels: labels, Value: 2}},
		"npu_chip_info_hbm_ecc_total_double_bit_error_cnt": {{Labels: labels, Value: 5}},
		"npu_chip_roce_verification_err_num":               {{Labels: labels, Value: 7}},
	}, nil)
	if dropped != 0 {
		t.Fatalf("dropped = %d", dropped)
	}
	assertSample(t, samples, "accelerator_health_status", "npu", 1)
	assertSample(t, samples, "accelerator_used_count", "npu", 1)
	assertSample(t, samples, "accelerator_total_count", "npu", 1)
	used := findSample(t, samples, "accelerator_used_count")
	if used.Labels["uuid"] != "Ascend-0" || used.Labels["pod"] != "job-0" || used.Labels["container"] != "main" {
		t.Fatalf("unexpected normalized NPU usage labels: %#v", used)
	}
	if sample := findSampleWithLabel(t, samples, "accelerator_error_total_count", "error_type", "hbm_ecc_double_bit"); sample.Value != 5 {
		t.Fatalf("unexpected HBM error sample: %#v", sample)
	}
	if sample := findSampleWithLabel(t, samples, "accelerator_error_total_count", "error_type", "roce_verification"); sample.Value != 7 {
		t.Fatalf("unexpected RoCE error sample: %#v", sample)
	}
}

func TestTransformNPUDistinguishesMemoryCapacityAndBandwidthUtilization(t *testing.T) {
	t.Parallel()
	source := model.Source{
		Name: "npu", AcceleratorType: model.AcceleratorTypeNPU,
		Labels: map[string]string{"node": "node-npu"},
	}
	labels := map[string]string{
		"id": "0", "vdie_id": "Ascend-0", "model_name": "Ascend910-Ascend-V1",
	}
	samples, dropped := Transform(source, model.Families{
		"npu_chip_info_hbm_utilization":           {{Labels: labels, Value: 4}},
		"npu_chip_info_hbm_bandwidth_utilization": {{Labels: labels, Value: 0}},
		"npu_chip_info_hbm_used_memory":           {{Labels: labels, Value: 2908}},
		"npu_chip_info_hbm_total_memory":          {{Labels: labels, Value: 65536}},
		"npu_chip_info_overall_utilization":       {{Labels: labels, Value: 17}},
	}, nil)
	if dropped != 0 {
		t.Fatalf("dropped = %d", dropped)
	}
	assertSample(t, samples, "accelerator_memory_bandwidth_utilization_percentage", "npu", 0)
	assertSample(t, samples, "accelerator_memory_utilization_percentage", "npu", 2908.0/65536.0*100)
	assertSample(t, samples, "accelerator_overall_utilization_percentage", "npu", 17)
}

func TestTransformNPUVGPUAndVNPU(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		source     model.Source
		families   model.Families
		inventory  Inventory
		wantDevice string
		wantModel  string
		wantUsed   float64
		wantTotal  float64
	}{
		{
			name:   "npu",
			source: model.Source{Name: "npu-a", AcceleratorType: model.AcceleratorTypeNPU, Labels: map[string]string{"node": "node-npu"}},
			families: model.Families{
				"npu_chip_info_hbm_used_memory":  {{Labels: map[string]string{"id": "0", "model_name": "Ascend 910B"}, Value: 4}},
				"npu_chip_info_hbm_total_memory": {{Labels: map[string]string{"id": "0", "model_name": "Ascend 910B"}, Value: 16}},
			},
			wantDevice: "0", wantModel: "Ascend 910B", wantUsed: 4 * bytesPerMiB, wantTotal: 16 * bytesPerMiB,
		},
		{
			name:   "vgpu",
			source: model.Source{Name: "vgpu-a", AcceleratorType: model.AcceleratorTypeVGPU},
			families: model.Families{
				"vGPU_device_memory_usage_in_bytes": {{Labels: map[string]string{"deviceuuid": "GPU-2", "vdeviceid": "slice-1", "podnamespace": "train", "podname": "job-1", "ctrname": "main"}, Value: 100}},
				"vGPU_device_memory_limit_in_bytes": {{Labels: map[string]string{"deviceuuid": "GPU-2", "vdeviceid": "slice-1", "podnamespace": "train", "podname": "job-1", "ctrname": "main"}, Value: 400}},
			},
			inventory:  Inventory{"GPU-2": {Node: "node-gpu", Model: "L40", MemoryTotalBytes: 800}},
			wantDevice: "GPU-2", wantModel: "L40", wantUsed: 100, wantTotal: 400,
		},
		{
			name:   "vnpu",
			source: model.Source{Name: "vnpu-a", AcceleratorType: model.AcceleratorTypeVNPU, Labels: map[string]string{"node": "node-vnpu"}},
			families: model.Families{
				"vnpu_pod_used_memory":  {{Labels: map[string]string{"v_dev_id": "100", "model_name": "Ascend 910B", "namespace": "train", "pod": "job-2"}, Value: 2}},
				"vnpu_pod_total_memory": {{Labels: map[string]string{"v_dev_id": "100", "model_name": "Ascend 910B", "namespace": "train", "pod": "job-2"}, Value: 8}},
			},
			wantDevice: "100", wantModel: "Ascend 910B", wantUsed: 2 * bytesPerKiB, wantTotal: 8 * bytesPerKiB,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			samples, dropped := Transform(test.source, test.families, test.inventory)
			if dropped != 0 {
				t.Fatalf("dropped = %d", dropped)
			}
			used := findSample(t, samples, "accelerator_memory_used_bytes")
			if used.Labels["device"] != test.wantDevice || used.Labels["model"] != test.wantModel || used.Value != test.wantUsed {
				t.Fatalf("unexpected used sample: %#v", used)
			}
			total := findSample(t, samples, "accelerator_memory_total_bytes")
			if total.Value != test.wantTotal {
				t.Fatalf("total value = %v, want %v", total.Value, test.wantTotal)
			}
			assertSample(t, samples, "accelerator_total_count", test.source.AcceleratorType, 1)
			if test.source.AcceleratorType == model.AcceleratorTypeVGPU || test.source.AcceleratorType == model.AcceleratorTypeVNPU {
				assertSample(t, samples, "accelerator_used_count", test.source.AcceleratorType, 1)
				assertSample(t, samples, "accelerator_partition_total_count", test.source.AcceleratorType, 1)
			}
			if test.source.AcceleratorType == model.AcceleratorTypeVGPU {
				assertSample(t, samples, "accelerator_share_percentage", test.source.AcceleratorType, 50)
			}
		})
	}
}

func TestBuildInventoryIncludesPhysicalMemoryCapacity(t *testing.T) {
	t.Parallel()
	labels := map[string]string{"Hostname": "node-a", "UUID": "GPU-1", "modelName": "A100"}
	inventory := BuildInventory([]Input{{
		Source: model.Source{Name: "gpu", AcceleratorType: model.AcceleratorTypeGPU},
		Families: model.Families{
			"DCGM_FI_DEV_FB_USED": {{Labels: labels, Value: 2}},
			"DCGM_FI_DEV_FB_FREE": {{Labels: labels, Value: 6}},
		},
	}})
	if got := inventory["GPU-1"]; got.Node != "node-a" || got.Model != "A100" || got.MemoryTotalBytes != 8*bytesPerMiB {
		t.Fatalf("unexpected inventory: %#v", got)
	}
}

func TestTransformDropsMissingNodeOrDeviceAndDefaultsModel(t *testing.T) {
	t.Parallel()
	source := model.Source{Name: "gpu", AcceleratorType: model.AcceleratorTypeGPU}
	families := model.Families{"DCGM_FI_DEV_GPU_UTIL": {
		{Labels: map[string]string{"Hostname": "node-a", "device": "0"}, Value: 1},
		{Labels: map[string]string{"Hostname": "node-a"}, Value: 2},
	}}
	samples, dropped := Transform(source, families, nil)
	if dropped != 1 {
		t.Fatalf("dropped = %d, want 1", dropped)
	}
	sample := findSample(t, samples, "accelerator_core_utilization_percentage")
	if sample.Labels["model"] != "unknown" {
		t.Fatalf("model = %q", sample.Labels["model"])
	}
}

func assertSample(t *testing.T, samples []model.Sample, name, acceleratorType string, value float64) {
	t.Helper()
	for _, sample := range samples {
		if sample.Name == name && sample.Labels["accelerator_type"] == acceleratorType && math.Abs(sample.Value-value) < 0.0001 {
			return
		}
	}
	t.Fatalf("sample %s{%s}=%v not found in %#v", name, acceleratorType, value, samples)
}

func findSample(t *testing.T, samples []model.Sample, name string) model.Sample {
	t.Helper()
	for _, sample := range samples {
		if sample.Name == name {
			return sample
		}
	}
	t.Fatalf("sample %s not found in %#v", name, samples)
	return model.Sample{}
}

func findSampleWithLabel(t *testing.T, samples []model.Sample, name, label, value string) model.Sample {
	t.Helper()
	for _, sample := range samples {
		if sample.Name == name && sample.Labels[label] == value {
			return sample
		}
	}
	t.Fatalf("sample %s{%s=%q} not found in %#v", name, label, value, samples)
	return model.Sample{}
}

func withLabel(labels map[string]string, name, value string) map[string]string {
	result := cloneLabels(labels)
	result[name] = value
	return result
}

func withLabels(labels, additions map[string]string) map[string]string {
	result := cloneLabels(labels)
	for name, value := range additions {
		result[name] = value
	}
	return result
}
