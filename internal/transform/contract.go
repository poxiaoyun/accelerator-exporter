package transform

import "sort"

const (
	MetricTypeGauge   = "gauge"
	MetricTypeCounter = "counter"
)

// MetricMetadata describes one metric in the normalized accelerator contract.
type MetricMetadata struct {
	Help string
	Type string
}

var canonicalMetrics = map[string]MetricMetadata{
	"accelerator_info":                                    {Help: "Static accelerator device information.", Type: MetricTypeGauge},
	"accelerator_total_count":                             {Help: "Accelerator device count represented as one series per device or partition.", Type: MetricTypeGauge},
	"accelerator_used_count":                              {Help: "Accelerator device usage represented as one for an assigned or active device.", Type: MetricTypeGauge},
	"accelerator_share_percentage":                        {Help: "Allocated accelerator share percentage.", Type: MetricTypeGauge},
	"accelerator_partition_total_count":                   {Help: "Accelerator partition count represented as one series per partition.", Type: MetricTypeGauge},
	"accelerator_core_utilization_percentage":             {Help: "Accelerator compute core utilization percentage.", Type: MetricTypeGauge},
	"accelerator_memory_bandwidth_utilization_percentage": {Help: "Accelerator memory bandwidth utilization percentage.", Type: MetricTypeGauge},
	"accelerator_io_utilization_percentage":               {Help: "Accelerator IO transfer utilization percentage.", Type: MetricTypeGauge},
	"accelerator_overall_utilization_percentage":          {Help: "Vendor-defined overall accelerator utilization percentage.", Type: MetricTypeGauge},
	"accelerator_memory_total_bytes":                      {Help: "Total accelerator memory in bytes.", Type: MetricTypeGauge},
	"accelerator_memory_used_bytes":                       {Help: "Used accelerator memory in bytes.", Type: MetricTypeGauge},
	"accelerator_memory_free_bytes":                       {Help: "Free accelerator memory in bytes.", Type: MetricTypeGauge},
	"accelerator_memory_utilization_percentage":           {Help: "Accelerator memory capacity utilization percentage.", Type: MetricTypeGauge},
	"accelerator_memory_oom_total_count":                  {Help: "Total accelerator memory out-of-memory events.", Type: MetricTypeCounter},
	"accelerator_health_status":                           {Help: "Accelerator health status, where one is healthy and zero is unhealthy.", Type: MetricTypeGauge},
	"accelerator_temperature_celsius":                     {Help: "Accelerator core temperature in degrees Celsius.", Type: MetricTypeGauge},
	"accelerator_power_usage_watts":                       {Help: "Current accelerator power usage in watts.", Type: MetricTypeGauge},
	"accelerator_power_limit_watts":                       {Help: "Accelerator enforced power limit in watts.", Type: MetricTypeGauge},
	"accelerator_throttle_reason_info":                    {Help: "Accelerator throttle reason information represented as one for an observed reason.", Type: MetricTypeGauge},
	"accelerator_error_total_count":                       {Help: "Total accelerator hardware and interconnect errors by error type.", Type: MetricTypeCounter},
	"accelerator_operations_total_count":                  {Help: "Total accelerator arithmetic operations.", Type: MetricTypeCounter},
	"accelerator_operations_per_second":                   {Help: "Accelerator arithmetic operations per second.", Type: MetricTypeGauge},
	"accelerator_batch_size_count":                        {Help: "Accelerator workload batch size.", Type: MetricTypeGauge},
	"accelerator_latency_seconds":                         {Help: "Accelerator workload execution latency in seconds.", Type: MetricTypeGauge},
	"accelerator_queue_depth_count":                       {Help: "Accelerator execution queue depth.", Type: MetricTypeGauge},
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
