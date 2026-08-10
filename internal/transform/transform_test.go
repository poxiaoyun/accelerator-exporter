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
			inventory:  Inventory{"GPU-2": {Node: "node-gpu", Model: "L40"}},
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
		})
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
