// Package transform maps vendor metrics to the accelerator metric contract.
package transform

import (
	"sort"
	"strings"

	"xiaoshiai.cn/rune/internal/model"
)

const (
	bytesPerMiB = 1024 * 1024
	bytesPerKiB = 1024
)

type metricMapping struct {
	target string
	factor float64
}

var mappings = map[string]map[string]metricMapping{
	model.AcceleratorTypeGPU: {
		"DCGM_FI_DEV_GPU_UTIL":             {target: "accelerator_core_utilization_percentage", factor: 1},
		"DCGM_FI_DEV_MEM_COPY_UTIL":        {target: "accelerator_memory_bandwidth_utilization_percentage", factor: 1},
		"DCGM_FI_DEV_FB_USED":              {target: "accelerator_memory_used_bytes", factor: bytesPerMiB},
		"DCGM_FI_DEV_FB_FREE":              {target: "accelerator_memory_free_bytes", factor: bytesPerMiB},
		"DCGM_FI_DEV_GPU_TEMP":             {target: "accelerator_temperature_celsius", factor: 1},
		"DCGM_FI_DEV_POWER_USAGE":          {target: "accelerator_power_usage_watts", factor: 1},
		"DCGM_FI_DEV_ENFORCED_POWER_LIMIT": {target: "accelerator_power_limit_watts", factor: 1},
	},
	model.AcceleratorTypeNPU: {
		"npu_chip_info_utilization":      {target: "accelerator_core_utilization_percentage", factor: 1},
		"npu_chip_info_hbm_utilization":  {target: "accelerator_memory_bandwidth_utilization_percentage", factor: 1},
		"npu_chip_info_hbm_used_memory":  {target: "accelerator_memory_used_bytes", factor: bytesPerMiB},
		"npu_chip_info_hbm_total_memory": {target: "accelerator_memory_total_bytes", factor: bytesPerMiB},
		"npu_chip_info_temperature":      {target: "accelerator_temperature_celsius", factor: 1},
		"npu_chip_info_power":            {target: "accelerator_power_usage_watts", factor: 1},
		"npu_chip_info_power_usage":      {target: "accelerator_power_usage_watts", factor: 1},
		"npu_chip_info_power_limit":      {target: "accelerator_power_limit_watts", factor: 1},
	},
	model.AcceleratorTypeVGPU: {
		"Device_utilization_desc_of_container": {target: "accelerator_core_utilization_percentage", factor: 1},
		"vGPU_device_memory_limit_in_bytes":    {target: "accelerator_memory_total_bytes", factor: 1},
		"vGPU_device_memory_usage_in_bytes":    {target: "accelerator_memory_used_bytes", factor: 1},
	},
	model.AcceleratorTypeVNPU: {
		"vnpu_pod_aicore_utilization": {target: "accelerator_core_utilization_percentage", factor: 1},
		"vnpu_pod_total_memory":       {target: "accelerator_memory_total_bytes", factor: bytesPerKiB},
		"vnpu_pod_used_memory":        {target: "accelerator_memory_used_bytes", factor: bytesPerKiB},
	},
}

// DeviceInfo is physical-device metadata used to enrich virtual metrics.
type DeviceInfo struct {
	Node  string
	Model string
}

// Inventory maps physical GPU UUIDs to their node and model.
type Inventory map[string]DeviceInfo

// Input is one successful source scrape used to build shared metadata.
type Input struct {
	Source   model.Source
	Families model.Families
}

// BuildInventory extracts physical GPU metadata from successful source scrapes.
func BuildInventory(inputs []Input) Inventory {
	inventory := Inventory{}
	for _, input := range inputs {
		source := input.Source
		families := input.Families
		if source.AcceleratorType != model.AcceleratorTypeGPU {
			continue
		}
		for _, samples := range families {
			for _, sample := range samples {
				uuid := first(sample.Labels, "UUID", "uuid")
				if uuid == "" {
					continue
				}
				inventory[uuid] = DeviceInfo{
					Node:  firstNonEmpty(first(sample.Labels, "node", "Hostname", "hostname"), source.Labels["node"]),
					Model: firstNonEmpty(first(sample.Labels, "model", "modelName", "model_name"), source.Labels["model"]),
				}
			}
		}
	}
	return inventory
}

// Transform converts one source scrape to normalized accelerator samples.
// It returns the converted samples and the number dropped for missing identity.
func Transform(source model.Source, families model.Families, inventory Inventory) ([]model.Sample, int) {
	sourceMappings := mappings[source.AcceleratorType]
	var samples []model.Sample
	dropped := 0
	for rawName, mapping := range sourceMappings {
		for _, raw := range families[rawName] {
			labels := normalizeLabels(source, raw.Labels, inventory)
			if labels["node"] == "" || labels["device"] == "" {
				dropped++
				continue
			}
			if labels["model"] == "" {
				labels["model"] = "unknown"
			}
			samples = append(samples, model.Sample{Name: mapping.target, Labels: labels, Value: raw.Value * mapping.factor})
		}
	}
	samples = append(samples, deriveMemoryMetrics(samples)...)
	samples = append(samples, deriveInfoMetrics(samples)...)
	sort.Slice(samples, func(i, j int) bool { return sampleKey(samples[i]) < sampleKey(samples[j]) })
	return samples, dropped
}

func normalizeLabels(source model.Source, raw map[string]string, inventory Inventory) map[string]string {
	labels := make(map[string]string, len(source.Labels)+10)
	for name, value := range source.Labels {
		if value != "" && name != "accelerator_type" {
			labels[name] = value
		}
	}
	labels["accelerator_type"] = source.AcceleratorType
	if labels["vendor"] == "" {
		switch source.AcceleratorType {
		case model.AcceleratorTypeGPU, model.AcceleratorTypeVGPU:
			labels["vendor"] = "nvidia"
		case model.AcceleratorTypeNPU, model.AcceleratorTypeVNPU:
			labels["vendor"] = "huawei"
		}
	}

	node := first(raw, "node", "Hostname", "hostname")
	modelName := first(raw, "model", "modelName", "model_name")
	device := first(raw, "device")
	switch source.AcceleratorType {
	case model.AcceleratorTypeGPU:
		device = first(raw, "UUID", "uuid", "device")
		if uuid := first(raw, "UUID", "uuid"); uuid != "" {
			labels["uuid"] = uuid
		}
	case model.AcceleratorTypeNPU:
		device = first(raw, "id", "device", "vdie_id")
	case model.AcceleratorTypeVGPU:
		device = first(raw, "deviceuuid", "UUID", "uuid", "device")
		if info, ok := inventory[device]; ok {
			node = firstNonEmpty(node, info.Node)
			modelName = firstNonEmpty(modelName, info.Model)
		}
		if partition := first(raw, "vdeviceid", "partition"); partition != "" {
			labels["partition"] = partition
		}
	case model.AcceleratorTypeVNPU:
		device = first(raw, "v_dev_id", "device", "id")
		if partition := first(raw, "v_dev_id", "partition"); partition != "" {
			labels["partition"] = partition
		}
	}
	setIfPresent(labels, "node", firstNonEmpty(node, labels["node"]))
	setIfPresent(labels, "device", firstNonEmpty(device, labels["device"]))
	setIfPresent(labels, "model", firstNonEmpty(modelName, labels["model"]))
	setIfPresent(labels, "namespace", first(raw, "namespace", "podnamespace", "exported_namespace"))
	setIfPresent(labels, "pod", first(raw, "pod", "podname"))
	setIfPresent(labels, "container", first(raw, "container", "ctrname"))
	setIfPresent(labels, "instance", first(raw, "app_instance", "rune_instance"))
	setIfPresent(labels, "driver_version", first(raw, "driver_version", "DCGM_FI_DRIVER_VERSION"))
	return labels
}

func deriveMemoryMetrics(samples []model.Sample) []model.Sample {
	type values struct {
		labels map[string]string
		used   *float64
		total  *float64
		free   *float64
	}
	byIdentity := map[string]*values{}
	for index := range samples {
		sample := &samples[index]
		if sample.Name != "accelerator_memory_used_bytes" && sample.Name != "accelerator_memory_total_bytes" && sample.Name != "accelerator_memory_free_bytes" {
			continue
		}
		key := labelsKey(sample.Labels)
		current := byIdentity[key]
		if current == nil {
			current = &values{labels: sample.Labels}
			byIdentity[key] = current
		}
		switch sample.Name {
		case "accelerator_memory_used_bytes":
			current.used = &sample.Value
		case "accelerator_memory_total_bytes":
			current.total = &sample.Value
		case "accelerator_memory_free_bytes":
			current.free = &sample.Value
		}
	}
	var derived []model.Sample
	for _, current := range byIdentity {
		if current.total == nil && current.used != nil && current.free != nil {
			value := *current.used + *current.free
			current.total = &value
			derived = append(derived, model.Sample{Name: "accelerator_memory_total_bytes", Labels: cloneLabels(current.labels), Value: value})
		}
		if current.free == nil && current.total != nil && current.used != nil {
			value := *current.total - *current.used
			if value < 0 {
				value = 0
			}
			derived = append(derived, model.Sample{Name: "accelerator_memory_free_bytes", Labels: cloneLabels(current.labels), Value: value})
		}
		if current.used != nil && current.total != nil && *current.total > 0 {
			derived = append(derived, model.Sample{
				Name: "accelerator_memory_utilization_percentage", Labels: cloneLabels(current.labels), Value: *current.used / *current.total * 100,
			})
		}
	}
	return derived
}

func deriveInfoMetrics(samples []model.Sample) []model.Sample {
	byDevice := map[string]model.Sample{}
	for _, sample := range samples {
		labels := map[string]string{}
		for _, name := range []string{"node", "device", "model", "partition", "uuid", "vendor", "accelerator_type", "driver_version"} {
			if value := sample.Labels[name]; value != "" {
				labels[name] = value
			}
		}
		info := model.Sample{Name: "accelerator_info", Labels: labels, Value: 1}
		byDevice[sampleKey(info)] = info
	}
	result := make([]model.Sample, 0, len(byDevice))
	for _, sample := range byDevice {
		result = append(result, sample)
	}
	return result
}

func first(labels map[string]string, names ...string) string {
	for _, name := range names {
		if value := labels[name]; value != "" {
			return value
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func setIfPresent(labels map[string]string, name, value string) {
	if value != "" {
		labels[name] = value
	}
}

func cloneLabels(labels map[string]string) map[string]string {
	cloned := make(map[string]string, len(labels))
	for name, value := range labels {
		cloned[name] = value
	}
	return cloned
}

func sampleKey(sample model.Sample) string {
	return sample.Name + "\xff" + labelsKey(sample.Labels)
}

func labelsKey(labels map[string]string) string {
	parts := make([]string, 0, len(labels))
	for name, value := range labels {
		parts = append(parts, name+"="+value)
	}
	sort.Strings(parts)
	return strings.Join(parts, "\xff")
}
