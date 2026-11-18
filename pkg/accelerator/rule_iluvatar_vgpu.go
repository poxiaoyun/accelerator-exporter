package accelerator

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
)

// GPU Manager v3.0.8 exposes per-card rows and a duplicate "total" row.
// container_gpu_memory_total measures USED memory in MiB despite its name.
// Its memory utilization uses physical capacity, not the virtual allocation.
// Neither that percentage nor the container-wide requested limit can express
// a virtual instance's memory capacity on each parent, so leave them absent.
// https://gitee.com/deep-spark/iluvatar-vgpu-manager/blob/v3.0.8/pkg/services/display/display.go
var iluvatarVGPURule = Rule{
	Name: RuleIluvatarVGPU, Vendor: VendorIluvatar,
	Targets: []Target{{
		Select: func(raw map[string]string) bool {
			return strings.HasPrefix(first(raw, "gpu", "gpu_memory"), "gpu")
		},
		Identify: identifyIluvatarVGPU,
		Metrics: []Metric{
			{Source: "container_gpu_utilization", Canonical: "accelerator_core_utilization_percentage", Factor: 1},
			{Source: "container_gpu_memory_total", Canonical: "accelerator_memory_used_bytes", Factor: bytesPerMiB},
		},
		NativeResources: []NativeResource{{
			Mode:      DeviceModeVirtual,
			NamesFrom: func(raw map[string]string) []string { return iluvatarCoreResources(raw["modelName"]) },
			DeviceID: func(env Environment, raw map[string]string) string {
				if identifyIluvatarVGPU(env, raw).DeviceID == "" {
					return ""
				}
				allocations := iluvatarContainerAllocations(env, raw)
				// One allocated core slot is enough to establish the ownership
				// relation. Its index can resolve to several physical parents.
				for _, allocation := range allocations {
					if strings.HasSuffix(allocation.NativeResource, ".vCore") {
						return allocation.NativeDeviceID
					}
				}
				return ""
			},
		}},
	}},
}

func iluvatarIndexKey(node, index string) string { return "iluvatar-index\xff" + node + "\xff" + index }

// An index is only a join key within the current successful scrape round. The
// public physical identifier always comes from IX's UUID, never from gpu0.
func buildIluvatarInventory(inventory Inventory, input InventoryInput) {
	for name, samples := range input.Families {
		if !strings.HasPrefix(name, "ix_") {
			continue
		}
		for _, sample := range samples {
			raw := sample.Labels
			node := firstNonEmpty(input.Source.Labels[LabelNode], raw["node_name"])
			if raw["uuid"] == "" || raw["gpu"] == "" || node == "" {
				continue
			}
			key := iluvatarIndexKey(node, raw["gpu"])
			info := DeviceInfo{DeviceID: raw["uuid"], Node: node, Model: iluvatarModel(raw["name"])}
			if previous, exists := inventory[key]; exists && previous.DeviceID != info.DeviceID {
				info.DeviceID = "" // Ambiguous indices cannot identify a parent.
			}
			inventory[key] = info
		}
	}
}

func identifyIluvatarVGPU(env Environment, raw map[string]string) Identity {
	node := firstNonEmpty(env.Source.Labels[LabelNode], raw["node"])
	index := strings.TrimPrefix(first(raw, "gpu", "gpu_memory"), "gpu")
	parent := env.Inventory[iluvatarIndexKey(node, index)]
	identity := Identity{Node: node, Vendor: VendorIluvatar, DeviceType: DeviceTypeGPU, DeviceMode: DeviceModeVirtual,
		Model: iluvatarModel(firstNonEmpty(raw["modelName"], parent.Model)), ParentDeviceID: parent.DeviceID}
	allocations := iluvatarContainerAllocations(env, raw)
	if len(allocations) == 0 || parent.DeviceID == "" {
		return identity
	}
	// The manager hashes the sorted core/memory device IDs to identify the
	// vcuda allocation (GenerateVcudaID). Suffix the physical UUID so a single
	// container spanning several cards has one observation target per parent.
	ids := make([]string, 0, len(allocations))
	for _, allocation := range allocations {
		ids = append(ids, allocation.NativeDeviceID)
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	hash := sha256.Sum256([]byte(strings.Join(ids, ",")))
	identity.DeviceID = hex.EncodeToString(hash[:]) + "-" + parent.DeviceID
	return identity
}

func iluvatarCoreResources(model string) []string {
	names := []string{"iluvatar.ai/" + model + ".vCore"}
	if iluvatarModel(model) == "mrc-v100" {
		names = []string{"iluvatar.ai/MRC-V100.vCore", "iluvatar.ai/MRC-V100-0x1B.vCore"}
	}
	return names
}

func iluvatarContainerAllocations(env Environment, raw map[string]string) []Allocation {
	if raw["namespace"] == "" || raw["pod_name"] == "" || raw["container_name"] == "" {
		return nil
	}
	cores := iluvatarCoreResources(raw["modelName"])
	var result []Allocation
	uid := ""
	for _, allocation := range env.Allocations {
		if allocation.Namespace != raw["namespace"] || allocation.Pod != raw["pod_name"] || allocation.Container != raw["container_name"] || allocation.PodUID == "" || allocation.NativeDeviceID == "" {
			continue
		}
		coreName := strings.TrimSuffix(allocation.NativeResource, ".vMem") + ".vCore"
		if strings.HasSuffix(allocation.NativeResource, ".vCore") {
			coreName = allocation.NativeResource
		}
		if !slices.Contains(cores, coreName) {
			continue
		}
		if uid != "" && uid != allocation.PodUID {
			return nil
		}
		uid = allocation.PodUID
		result = append(result, allocation)
	}
	slices.SortFunc(result, func(a, b Allocation) int { return strings.Compare(a.NativeDeviceID, b.NativeDeviceID) })
	return result
}
