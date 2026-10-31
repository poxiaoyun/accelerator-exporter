// Package accelerator owns the vendor-neutral accelerator metric contract, the
// device vocabulary, and the per-vendor rules that map native exporter metrics
// onto the contract.
//
// The contract has three layers, defined in specification/Monitoring.md:
//
//   - 设备层 (device layer) — accelerator_* metrics carrying only the identity,
//     classification and position label groups. The device layer never carries
//     ownership labels.
//   - 关系层 (relation layer) — accelerator_pod_info, one series per observation
//     target and Pod binding, sourced from the device allocation fact.
//   - 归因层 (attribution layer) — <domain>_accelerator_workload_* metrics. The
//     exporter does not produce them; they are joined from the two layers above.
//
// A Rule is a stateless interpretation of one scrape source. Rules live one
// file per vendor, mirroring cloud/pkg/noderesource's rule_<vendor>.go
// convention.
package accelerator

import (
	"strings"
)

// Label names of the device layer. Specification/Monitoring.md fixes these; the
// single emission point is Identity.deviceLabels.
const (
	LabelNode           = "node"
	LabelDeviceID       = "device_id"
	LabelParentDeviceID = "parent_device_id"
	LabelDeviceType     = "device_type"
	LabelDeviceMode     = "device_mode"
	LabelDeviceIndex    = "device_index"
	LabelVendor         = "vendor"
	LabelModel          = "model"

	// Static information carried only by accelerator_info.
	LabelProfile         = "profile"
	LabelDriverVersion   = "driver_version"
	LabelFirmwareVersion = "firmware_version"
	LabelSerialNumber    = "serial_number"
	LabelPCIBusID        = "pci_bus_id"
	LabelArchitecture    = "architecture"

	// Relation-layer labels carried only by accelerator_pod_info.
	LabelNamespace = "namespace"
	LabelPod       = "pod"
	LabelPodUID    = "pod_uid"
	LabelContainer = "container"
)

// Device classes. device_type takes the lower-case form of the semantic layer's
// resource type; a class Cloud does not know is still allowed.
const (
	DeviceTypeGPU = "gpu"
	DeviceTypeNPU = "npu"
	DeviceTypeDCU = "dcu"
	DeviceTypePPU = "ppu"
)

// Device modes. device_mode says how an observation target relates to the
// physical device and is independent of device_type.
const (
	DeviceModePhysical  = "physical"
	DeviceModePartition = "partition"
	DeviceModeVirtual   = "virtual"
)

// Vendor names, lower-cased. An unrecognised vendor is VendorUnknown.
const (
	VendorNVIDIA  = "nvidia"
	VendorHuawei  = "huawei"
	VendorHygon   = "hygon"
	VendorUnknown = "unknown"
)

// Health state values of accelerator_health_state.
const (
	HealthStateHealthy  = "healthy"
	HealthStateDegraded = "degraded"
	HealthStateFault    = "fault"
	HealthStateUnknown  = "unknown"
)

// Diagnostic severity values of accelerator_health_diagnostic_info.
const (
	SeverityInfo     = "info"
	SeverityWarning  = "warning"
	SeverityCritical = "critical"
)

// ModelUnknown is the mandatory fallback for an unidentifiable model.
const ModelUnknown = "unknown"

// Built-in Rule names. These are the only accepted values of a source's rule
// field, and they match the presets in deploy/accelerator-exporter/values.yaml.
const (
	RuleNvidiaDCGM  = "nvidia-dcgm"
	RuleAscendNPU   = "ascend-npu"
	RuleVolcanoVGPU = "volcano-vgpu"
	RuleHygonDCU    = "hygon-dcu"
)

// isDeviceClass reports whether the value is a class Cloud understands.
func isDeviceClass(deviceType string) bool {
	switch deviceType {
	case DeviceTypeGPU, DeviceTypeNPU, DeviceTypeDCU, DeviceTypePPU:
		return true
	default:
		return false
	}
}

// isDeviceMode reports whether the value is a declared device mode.
func isDeviceMode(deviceMode string) bool {
	switch deviceMode {
	case DeviceModePhysical, DeviceModePartition, DeviceModeVirtual:
		return true
	default:
		return false
	}
}

// isHealthState reports whether the value is a declared health state.
func isHealthState(state string) bool {
	switch state {
	case HealthStateHealthy, HealthStateDegraded, HealthStateFault, HealthStateUnknown:
		return true
	default:
		return false
	}
}

// isSeverity reports whether the value is a declared diagnostic severity.
func isSeverity(severity string) bool {
	switch severity {
	case SeverityInfo, SeverityWarning, SeverityCritical:
		return true
	default:
		return false
	}
}

// isDecimal reports whether the value is a bare decimal number. Specification
// fixes that device_id must not be one: an index identifies a position, not an
// identity.
func isDecimal(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

// labelValue keeps a vendor-supplied value inside the label value rules: lower
// case, and only letters, digits, underscore, dot and dash. It returns an empty
// string when nothing survives, so callers can omit the label instead of
// emitting an invalid one.
func labelValue(value string) string {
	var builder strings.Builder
	previousSeparator := false
	for _, character := range strings.ToLower(value) {
		switch {
		case character >= 'a' && character <= 'z',
			character >= '0' && character <= '9',
			character == '.', character == '_', character == '-':
			builder.WriteRune(character)
			previousSeparator = false
		default:
			if !previousSeparator && builder.Len() > 0 {
				builder.WriteByte('-')
				previousSeparator = true
			}
		}
	}
	return strings.Trim(builder.String(), "-._")
}
