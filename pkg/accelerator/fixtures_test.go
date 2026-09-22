package accelerator

import "testing"

// The fixture corpus is the shared input of the golden test and the vendor
// tests. Every fixture is a realistic subset of what the vendor endpoint
// exposes, including the labels that must NOT reach the public contract, so a
// leak is caught here rather than in production.

type fixture struct {
	name      string
	source    Source
	families  Families
	inventory Inventory
}

var fixtures = []fixture{
	{
		name:   "dcgm-physical-gpu",
		source: Source{Name: "nvidia-dcgm-exporter/node-gpu", Rule: RuleNvidiaDCGM},
		families: Families{
			"DCGM_FI_DEV_GPU_UTIL":             {{Labels: dcgmLabels(), Value: 75}},
			"DCGM_FI_DEV_MEM_COPY_UTIL":        {{Labels: dcgmLabels(), Value: 30}},
			"DCGM_FI_DEV_FB_USED":              {{Labels: dcgmLabels(), Value: 20480}},
			"DCGM_FI_DEV_FB_FREE":              {{Labels: dcgmLabels(), Value: 59802}},
			"DCGM_FI_DEV_FB_RESERVED":          {{Labels: dcgmLabels(), Value: 1638}},
			"DCGM_FI_DEV_GPU_TEMP":             {{Labels: dcgmLabels(), Value: 61}},
			"DCGM_FI_DEV_POWER_USAGE":          {{Labels: dcgmLabels(), Value: 210}},
			"DCGM_FI_DEV_ENFORCED_POWER_LIMIT": {{Labels: dcgmLabels(), Value: 300}},
			"DCGM_EXP_GPU_HEALTH_STATUS": {{Labels: withLabels(dcgmLabels(), map[string]string{
				"health_error_severity": "ISOLATE", "health_error_category": "HARDWARE_MEMORY",
				"health_error_code": "DCGM_FR_VOLATILE_DBE_DETECTED",
			}), Value: 20}},
			"DCGM_FI_DEV_XID_ERRORS":          {{Labels: dcgmLabels(), Value: 0}},
			"DCGM_FI_DEV_ROW_REMAP_FAILURE":   {{Labels: dcgmLabels(), Value: 0}},
			"DCGM_EXP_XID_ERRORS_TOTAL":       {{Labels: withLabel(dcgmLabels(), "xid", "31"), Value: 3}},
			"DCGM_EXP_CLOCK_EVENTS_COUNT":     {{Labels: withLabel(dcgmLabels(), "clock_event", "thermal"), Value: 2}},
			"DCGM_FI_DEV_PCIE_REPLAY_COUNTER": {{Labels: dcgmLabels(), Value: 4}},
			"DCGM_FI_DEV_ECC_DBE_VOL_TOTAL":   {{Labels: dcgmLabels(), Value: 1}},
		},
	},
	{
		name:   "dcgm-mig-instance",
		source: Source{Name: "nvidia-dcgm-exporter/node-gpu", Rule: RuleNvidiaDCGM},
		families: Families{
			"DCGM_FI_DEV_GPU_UTIL":      {{Labels: migLabels(), Value: 40}},
			"DCGM_FI_DEV_FB_USED":       {{Labels: migLabels(), Value: 1024}},
			"DCGM_FI_DEV_FB_FREE":       {{Labels: migLabels(), Value: 9216}},
			"DCGM_FI_DEV_GPU_TEMP":      {{Labels: migLabels(), Value: 58}},
			"DCGM_FI_DEV_POWER_USAGE":   {{Labels: migLabels(), Value: 90}},
			"DCGM_EXP_XID_ERRORS_TOTAL": {{Labels: withLabel(migLabels(), "xid", "13"), Value: 2}},
		},
	},
	{
		name:   "ascend-npu",
		source: Source{Name: "npu-exporter/node-npu", Rule: RuleAscendNPU, Labels: map[string]string{"node": "node-npu"}},
		families: Families{
			"npu_chip_info_utilization":                        {{Labels: npuLabels(), Value: 55}},
			"npu_chip_info_hbm_bandwidth_utilization":          {{Labels: npuLabels(), Value: 12}},
			"npu_chip_info_overall_utilization":                {{Labels: npuLabels(), Value: 41}},
			"npu_chip_info_hbm_used_memory":                    {{Labels: npuLabels(), Value: 2908}},
			"npu_chip_info_hbm_total_memory":                   {{Labels: npuLabels(), Value: 65536}},
			"npu_chip_info_temperature":                        {{Labels: npuLabels(), Value: 52}},
			"npu_chip_info_power":                              {{Labels: npuLabels(), Value: 180}},
			"npu_chip_info_power_limit":                        {{Labels: npuLabels(), Value: 300}},
			"npu_chip_info_health_status":                      {{Labels: npuLabels(), Value: 1}},
			"npu_chip_info_hbm_ecc_total_double_bit_error_cnt": {{Labels: npuLabels(), Value: 5}},
			"npu_chip_roce_verification_err_num":               {{Labels: npuLabels(), Value: 7}},
		},
	},
	{
		name:   "ascend-vnpu",
		source: Source{Name: "npu-exporter/node-npu", Rule: RuleAscendNPU, Labels: map[string]string{"node": "node-npu"}},
		families: Families{
			"vnpu_pod_aicore_utilization": {{Labels: vnpuLabels(), Value: 33}},
			"vnpu_pod_used_memory":        {{Labels: vnpuLabels(), Value: 2048}},
			"vnpu_pod_total_memory":       {{Labels: vnpuLabels(), Value: 8192}},
		},
	},
	{
		name:      "volcano-vgpu",
		source:    Source{Name: "volcano-vgpu-device-plugin/node-gpu", Rule: RuleVolcanoVGPU},
		inventory: Inventory{"GPU-2": {Node: "node-gpu", Model: "L40"}},
		families: Families{
			"Device_utilization_desc_of_container": {{Labels: vgpuLabels(), Value: 66}},
			"vGPU_device_memory_usage_in_bytes":    {{Labels: vgpuLabels(), Value: 4294967296}},
			"vGPU_device_memory_limit_in_bytes":    {{Labels: vgpuLabels(), Value: 8589934592}},
		},
	},
	{
		name:   "canonical-passthrough",
		source: Source{Name: "custom/node-gpu", Rule: RuleNvidiaDCGM},
		families: Families{
			"accelerator_io_utilization_percentage": {{Labels: withLabel(dcgmLabels(), "quantile", "0.99"), Value: 12}},
		},
	},
	{
		name:     "missing-identity",
		source:   Source{Name: "nvidia-dcgm-exporter/node-gpu", Rule: RuleNvidiaDCGM},
		families: Families{"DCGM_FI_DEV_GPU_UTIL": {{Labels: map[string]string{"Hostname": "node-a"}, Value: 1}}},
	},
}

func dcgmLabels() map[string]string {
	return map[string]string{
		"Hostname": "node-gpu", "UUID": "GPU-1", "modelName": "NVIDIA A100-SXM4-80GB", "device": "0",
		"pci_bus_id": "00000000:07:00.0", "DCGM_FI_DRIVER_VERSION": "550.90.07",
		"namespace": "train", "pod": "job-0", "container": "main",
	}
}

func migLabels() map[string]string {
	return map[string]string{
		"Hostname": "node-gpu", "UUID": "GPU-1", "modelName": "NVIDIA A100-SXM4-80GB", "device": "0",
		"GPU_I_ID": "3", "GPU_I_PROFILE": "1g.10gb", "pci_bus_id": "00000000:07:00.0",
		"DCGM_FI_DRIVER_VERSION": "550.90.07",
		"namespace":              "train", "pod": "job-1", "container": "main",
	}
}

func npuLabels() map[string]string {
	return map[string]string{
		"id": "0", "vdie_id": "Ascend-0", "model_name": "Ascend 910B",
		"namespace": "train", "pod": "job-2", "container": "main",
	}
}

func vnpuLabels() map[string]string {
	return map[string]string{
		"v_dev_id": "100", "model_name": "Ascend 910B", "namespace": "train", "pod": "job-3",
	}
}

func vgpuLabels() map[string]string {
	return map[string]string{
		"deviceuuid": "GPU-2", "vdeviceid": "slice-1", "modelName": "L40",
		"podnamespace": "train", "podname": "job-4", "ctrname": "main",
	}
}

// applyFixture runs the fixture through the rule its source names.
func applyFixture(t *testing.T, f fixture) Result {
	t.Helper()
	rule, ok := RuleByName(f.source.Rule)
	if !ok {
		t.Fatalf("fixture %q names unknown rule %q", f.name, f.source.Rule)
		return Result{}
	}
	return rule.Apply(f.source, f.families, f.inventory)
}

func withLabel(labels map[string]string, name, value string) map[string]string {
	result := withLabels(labels, nil)
	result[name] = value
	return result
}

func withLabels(labels, additions map[string]string) map[string]string {
	result := make(map[string]string, len(labels)+len(additions))
	for name, value := range labels {
		result[name] = value
	}
	for name, value := range additions {
		result[name] = value
	}
	return result
}

// ruleFor resolves a built-in rule by name for tests.
func ruleFor(t *testing.T, name string) Rule {
	t.Helper()
	rule, ok := RuleByName(name)
	if !ok {
		t.Fatalf("unknown rule %q", name)
	}
	return rule
}
