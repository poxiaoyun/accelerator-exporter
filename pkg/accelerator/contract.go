package accelerator

import (
	"slices"
	"sort"
)

// Metric type values of the Prometheus exposition format.
const (
	MetricTypeGauge   = "gauge"
	MetricTypeCounter = "counter"
)

// MetricMetadata declares one metric of the normalized accelerator contract.
//
// Every field is either fixed by specification/Monitoring.md or derived from
// it, so the contract is the single place that decides what a metric may carry.
// Rules map native metrics onto these names and do not restate the constraints.
type MetricMetadata struct {
	// Help is the HELP text of the metric.
	Help string
	// Type is the Prometheus metric type.
	Type string
	// Unit is the normalized unit. Consumers never convert units, so every
	// rule must convert into this unit itself.
	Unit string
	// ValueDomain is the closed set of values, or a range for continuous units.
	ValueDomain string
	// Modes lists the device modes this metric applies to. A nil slice means
	// every mode. A metric marked 物理设备 in the specification carries
	// DeviceModePhysical alone, and is never copied onto a partition or a
	// virtual instance.
	Modes []string
	// Enums lists the enum labels the metric may carry. They are the only
	// labels beyond the identity, classification and position groups that a
	// device-layer metric is allowed to expose.
	Enums []string
	// Info marks a metric that reports presence rather than a measurement. The
	// specification fixes its value at 1, so a rule selects which series exist
	// and the pipeline writes the value.
	Info bool
	// Layer names the contract layer that carries the metric.
	Layer string
	// Inventory marks the metric that is the only source of the observation
	// target inventory.
	Inventory bool
}

// Contract layers.
const (
	LayerDevice   = "device"
	LayerRelation = "relation"
)

// physicalOnly is the applicable-mode set of metrics the specification marks as
// describing the physical device itself.
var physicalOnly = []string{DeviceModePhysical}

var canonicalMetrics = map[string]MetricMetadata{
	"accelerator_info": {
		Help: "Observation target inventory and static device information.", Type: MetricTypeGauge,
		Unit: "1", ValueDomain: "always 1", Info: true, Layer: LayerDevice, Inventory: true,
	},
	"accelerator_pod_info": {
		Help: "Allocation relationship between an observation target and a Pod.", Type: MetricTypeGauge,
		Unit: "1", ValueDomain: "always 1", Info: true, Layer: LayerRelation,
	},

	"accelerator_core_utilization_percentage": {
		Help: "Accelerator compute core utilization percentage.", Type: MetricTypeGauge,
		Unit: "percent", ValueDomain: "0-100", Layer: LayerDevice,
	},
	"accelerator_memory_bandwidth_utilization_percentage": {
		Help: "Accelerator memory bandwidth utilization percentage.", Type: MetricTypeGauge,
		Unit: "percent", ValueDomain: "0-100", Layer: LayerDevice,
	},
	"accelerator_io_utilization_percentage": {
		Help: "Accelerator IO transfer utilization percentage.", Type: MetricTypeGauge,
		Unit: "percent", ValueDomain: "0-100", Layer: LayerDevice,
	},
	"accelerator_overall_utilization_percentage": {
		Help: "Vendor-defined overall accelerator utilization percentage.", Type: MetricTypeGauge,
		Unit: "percent", ValueDomain: "0-100", Layer: LayerDevice,
	},

	"accelerator_memory_total_bytes": {
		Help: "Total accelerator memory in bytes. For a virtual instance this is the limit, not capacity.", Type: MetricTypeGauge,
		Unit: "bytes", ValueDomain: ">= 0", Layer: LayerDevice,
	},
	"accelerator_memory_used_bytes": {
		Help: "Used accelerator memory in bytes.", Type: MetricTypeGauge,
		Unit: "bytes", ValueDomain: ">= 0", Layer: LayerDevice,
	},
	"accelerator_memory_free_bytes": {
		Help: "Free accelerator memory in bytes.", Type: MetricTypeGauge,
		Unit: "bytes", ValueDomain: ">= 0", Layer: LayerDevice,
	},
	"accelerator_memory_utilization_percentage": {
		Help: "Accelerator memory capacity utilization percentage.", Type: MetricTypeGauge,
		Unit: "percent", ValueDomain: "0-100", Layer: LayerDevice,
	},
	"accelerator_memory_oom_events_total": {
		Help: "Total accelerator memory out-of-memory events.", Type: MetricTypeCounter,
		Unit: "count", ValueDomain: "monotonic", Layer: LayerDevice,
	},

	"accelerator_health_state": {
		Help: "Accelerator health state, exactly one state per observation target.", Type: MetricTypeGauge,
		Unit: "1", ValueDomain: "always 1", Enums: []string{"state"}, Info: true, Layer: LayerDevice,
	},
	"accelerator_health_diagnostic_info": {
		Help: "Current accelerator health diagnostic, one series per observation target and diagnostic.", Type: MetricTypeGauge,
		Unit: "1", ValueDomain: "always 1", Enums: []string{"category", "code", "severity"}, Info: true, Layer: LayerDevice,
	},
	"accelerator_throttle_reason_info": {
		Help: "Current accelerator throttle reason, one series per observed reason.", Type: MetricTypeGauge,
		Unit: "1", ValueDomain: "always 1", Modes: physicalOnly, Enums: []string{"reason"}, Info: true, Layer: LayerDevice,
	},
	"accelerator_errors_total": {
		Help: "Total accelerator hardware and interconnect errors by error type.", Type: MetricTypeCounter,
		Unit: "count", ValueDomain: "monotonic", Modes: physicalOnly, Enums: []string{"error_type", "error_code"}, Layer: LayerDevice,
	},

	"accelerator_temperature_celsius": {
		Help: "Accelerator core temperature in degrees Celsius.", Type: MetricTypeGauge,
		Unit: "celsius", ValueDomain: "continuous", Modes: physicalOnly, Layer: LayerDevice,
	},
	"accelerator_power_usage_watts": {
		Help: "Current accelerator power usage in watts.", Type: MetricTypeGauge,
		Unit: "watts", ValueDomain: ">= 0", Modes: physicalOnly, Layer: LayerDevice,
	},
	"accelerator_power_limit_watts": {
		Help: "Accelerator enforced power limit in watts.", Type: MetricTypeGauge,
		Unit: "watts", ValueDomain: ">= 0", Modes: physicalOnly, Layer: LayerDevice,
	},
}

// CanonicalMetric returns metadata for a normalized accelerator metric.
func CanonicalMetric(name string) (MetricMetadata, bool) {
	metadata, ok := canonicalMetrics[name]
	return metadata, ok
}

// CanonicalMetricNames returns the normalized metric names in stable order.
func CanonicalMetricNames() []string {
	names := make([]string, 0, len(canonicalMetrics))
	for name := range canonicalMetrics {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// InventoryMetricName is the only source of the observation target inventory.
const InventoryMetricName = "accelerator_info"

// PodInfoMetricName is the relation-layer metric.
const PodInfoMetricName = "accelerator_pod_info"

// Contract violation reasons reported by the exporter. A violation is a
// contract breach observed in an actual scrape, not a configuration error.
const (
	ViolationUnknownMetric       = "unknown_metric"
	ViolationInapplicableMode    = "inapplicable_mode"
	ViolationMissingIdentity     = "missing_identity"
	ViolationMissingParentID     = "missing_parent_device_id"
	ViolationDecimalDeviceID     = "decimal_device_id"
	ViolationUnexpectedIndex     = "unexpected_device_index"
	ViolationInvalidEnum         = "invalid_enum"
	ViolationMissingRelationPart = "missing_relation_label"
)

// Validate checks the sample against the contract and returns every reason it
// breaches it.
//
// A measurement is not thrown away because its identity is incomplete: the
// device the reading came from is worth reporting even when the source could
// not name it. In that case the sample is kept and the gap is returned as a
// reason, so the exporter can count and expose it instead of hiding it.
//
// Only conditions that make the series meaningless drop a sample. The metric
// name must be in the contract, because the exposition format needs its type
// and help. A metric the contract marks as describing the physical device must
// not appear on a partition or a virtual instance, because that would present
// the parent device's reading as the instance's own. And a presence metric must
// carry a value from its declared closed set, because that label is what the
// series means.
func (sample Sample) Validate() (drop bool, reasons []string) {
	metadata, ok := CanonicalMetric(sample.Name)
	if !ok {
		return true, []string{ViolationUnknownMetric}
	}
	if metadata.Layer == LayerRelation {
		return validateRelation(sample)
	}

	labels := sample.Labels
	deviceMode := labels[LabelDeviceMode]
	if deviceMode != "" && !isDeviceMode(deviceMode) {
		return true, []string{ViolationInvalidEnum}
	}
	if len(metadata.Modes) > 0 && isDeviceMode(deviceMode) && !slices.Contains(metadata.Modes, deviceMode) {
		return true, []string{ViolationInapplicableMode}
	}

	if labels[LabelNode] == "" || labels[LabelDeviceID] == "" || labels[LabelDeviceType] == "" || deviceMode == "" {
		reasons = append(reasons, ViolationMissingIdentity)
	}
	if isDecimal(labels[LabelDeviceID]) {
		reasons = append(reasons, ViolationDecimalDeviceID)
	}
	if labels[LabelDeviceType] != "" && !isDeviceClass(labels[LabelDeviceType]) {
		reasons = append(reasons, ViolationInvalidEnum)
	}
	switch deviceMode {
	case DeviceModePhysical:
		if labels[LabelDeviceIndex] != "" {
			reasons = append(reasons, ViolationUnexpectedIndex)
		}
	case DeviceModePartition, DeviceModeVirtual:
		if labels[LabelParentDeviceID] == "" {
			reasons = append(reasons, ViolationMissingParentID)
		}
	}
	if sample.Name == "accelerator_health_state" && !isHealthState(labels["state"]) {
		return true, append(reasons, ViolationInvalidEnum)
	}
	if sample.Name == "accelerator_health_diagnostic_info" && !isSeverity(labels["severity"]) {
		return true, append(reasons, ViolationInvalidEnum)
	}
	return false, reasons
}

// validateRelation checks the relation layer, whose key is the full label set
// it carries. Its labels are all produced by the exporter from the allocation
// fact, so a missing one is a defect rather than an upstream gap.
func validateRelation(sample Sample) (bool, []string) {
	for _, name := range []string{
		LabelNode, LabelDeviceID, LabelNamespace, LabelPod, LabelPodUID, LabelContainer,
	} {
		if sample.Labels[name] == "" {
			return true, []string{ViolationMissingRelationPart}
		}
	}
	return false, nil
}
