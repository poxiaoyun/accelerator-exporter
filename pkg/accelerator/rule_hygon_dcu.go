package accelerator

import (
	"fmt"
	"math"
	"strconv"
)

// DCU 2.4 metric names and labels: official DCU Kubernetes component guide,
// section 3.1 (BW series included in section 1.3):
// https://download.sourcefind.cn:65024/file/5/Kubernetes%E6%8F%92%E4%BB%B6/DCU%20Kubernetes%E7%BB%84%E4%BB%B6%E7%94%A8%E6%88%B7%E6%8C%87%E5%8D%97.pdf
// The guide's cu_usedmemory_bytes is a typo. Emitted names and byte units:
// https://github.com/Project-HAMi/dcu-exporter/blob/30408d074cd420729f5698710d54fb30abefb0b1/main.go
// Allocation IDs and profile naming:
// https://github.com/kinglong08/k8s-dcu-device-plugin/tree/25aff110ac522b07f43d9713493ac94a944e820d/internal/pkg
// HCU 3.x has different resource and metric names; it is not this protocol.
var hygonDCURule = Rule{
	Name: RuleHygonDCU, Vendor: VendorHygon,
	Targets: []Target{
		{
			Identify: identifyHygonDCU,
			Metrics: []Metric{
				{Source: "dcu_utilizationrate", Canonical: "accelerator_core_utilization_percentage", Factor: 1},
				{Source: "dcu_usedmemory_bytes", Canonical: "accelerator_memory_used_bytes", Factor: 1},
				{Source: "dcu_memorycap_bytes", Canonical: "accelerator_memory_total_bytes", Factor: 1},
				{Source: "dcu_temp", Canonical: "accelerator_temperature_celsius", Factor: 1},
				{Source: "dcu_power_usage", Canonical: "accelerator_power_usage_watts", Factor: 1},
				{Source: "dcu_powercap", Canonical: "accelerator_power_limit_watts", Factor: 1},
			},
			NativeResources: []NativeResource{{
				Names: []string{"hygon.com/dcu"}, Mode: DeviceModePhysical,
				// The standard plugin allocates by PCI address, not serial.
				DeviceID: func(_ Environment, raw map[string]string) string {
					if raw["device_id"] == "" {
						return ""
					}
					return raw["pcieBus_number"]
				},
			}},
		},
		{
			Identify: identifyHygonVDCU,
			Metrics: []Metric{
				{Source: "vdcu_utilizationrate", Canonical: "accelerator_core_utilization_percentage", Factor: 1},
				{Source: "vdcu_usedmemory_bytes", Canonical: "accelerator_memory_used_bytes", Factor: 1},
			},
			Derive: []Deriver{deriveHygonVDCULimits},
			NativeResources: []NativeResource{{
				NamesFrom: hygonVDCUResources, Mode: DeviceModeVirtual,
				DeviceID: func(_ Environment, raw map[string]string) string {
					if hygonVDCUID(raw) != "" {
						return "vdev" + raw["vdcu_minor_number"]
					}
					return ""
				},
			}},
		},
	},
}

func identifyHygonDCU(env Environment, raw map[string]string) Identity {
	return Identity{
		Node:       firstNonEmpty(raw["node"], env.Source.Labels[LabelNode]),
		Model:      labelValue(firstNonEmpty(raw["name"], env.Source.Labels[LabelModel])),
		DeviceType: DeviceTypeDCU, DeviceMode: DeviceModePhysical, Vendor: VendorHygon,
		DeviceID: raw["device_id"], PCIBusID: raw["pcieBus_number"],
	}
}

func identifyHygonVDCU(env Environment, raw map[string]string) Identity {
	identity := identifyHygonDCU(env, raw)
	identity.DeviceMode = DeviceModeVirtual
	identity.ParentDeviceID = identity.DeviceID
	identity.DeviceID = hygonVDCUID(raw)
	identity.PCIBusID = ""
	return identity
}

func hygonVDCUID(raw map[string]string) string {
	parent, index := raw["device_id"], raw["vdcu_minor_number"]
	if parent == "" || !isDecimal(index) {
		return ""
	}
	return parent + "." + index
}

// A pre-split vDCU is registered under its CU/memory profile. Memory is bytes
// on the exporter label; the plugin rounds it to GiB in the resource name.
func hygonVDCUResources(raw map[string]string) []string {
	cu, err := strconv.ParseUint(raw["vdcu_computer_unit"], 10, 64)
	if err != nil || cu == 0 {
		return nil
	}
	memory, err := strconv.ParseUint(raw["vdcu_memory_cap"], 10, 64)
	if err != nil || memory == 0 {
		return nil
	}
	return []string{fmt.Sprintf("hygon.com/dcu-share-%dc-%.0fg", cu, math.Round(float64(memory)/(1024*1024*1024)))}
}

// The limit is a numeric LABEL rather than a metric. Parse it at the source,
// then let shared memory derivation compute used/limit * 100. Neither missing
// limits nor invalid labels are replaced by the physical card's capacity.
func deriveHygonVDCULimits(raw Families, converted []Sample, identities map[string]Identity) []Sample {
	limits := map[string]uint64{}
	invalid := map[string]bool{}
	for _, name := range []string{"vdcu_usedmemory_bytes", "vdcu_utilizationrate"} {
		for _, sample := range raw[name] {
			id := hygonVDCUID(sample.Labels)
			limit, err := strconv.ParseUint(sample.Labels["vdcu_memory_cap"], 10, 64)
			if id == "" || err != nil || limit == 0 {
				continue
			}
			if previous, ok := limits[id]; ok && previous != limit {
				invalid[id] = true
			}
			limits[id] = limit
		}
	}
	existing := sampleKeys(converted)
	var samples []Sample
	for _, identity := range identities {
		limit, ok := limits[identity.DeviceID]
		if !ok || invalid[identity.DeviceID] {
			continue
		}
		samples = appendUnique(samples, existing, Sample{
			Name: "accelerator_memory_total_bytes", Labels: identity.deviceLabels(), Value: float64(limit),
		})
	}
	return samples
}
