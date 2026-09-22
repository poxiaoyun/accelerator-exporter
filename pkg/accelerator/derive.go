package accelerator

// This file holds derivation shared by every rule. Vendor semantics stay in the
// vendor files; what lives here is only the part that is the same for every
// source, or that the specification fixes exactly once.

// deviceLayerBase reduces a sample's labels to the identity, classification and
// position groups, dropping any enum label the metric carries.
func deviceLayerBase(labels map[string]string) map[string]string {
	return identityFromLabels(labels).deviceLabels()
}

// deriveMemoryMetrics fills accelerator_memory_total_bytes or
// accelerator_memory_free_bytes from the other two, and derives the capacity
// utilization from used and total.
//
// Values are grouped per observation target rather than per label set, so one
// target produces one set of memory series instead of one copy per row the
// vendor metric happened to carry.
func deriveMemoryMetrics(samples []Sample) []Sample {
	type values struct {
		labels map[string]string
		used   *float64
		total  *float64
		free   *float64
	}
	byTarget := map[string]*values{}
	existing := sampleKeys(samples)
	for index := range samples {
		sample := &samples[index]
		switch sample.Name {
		case "accelerator_memory_used_bytes", "accelerator_memory_total_bytes", "accelerator_memory_free_bytes":
		default:
			continue
		}
		labels := deviceLayerBase(sample.Labels)
		key := labelsKey(labels)
		current := byTarget[key]
		if current == nil {
			current = &values{labels: labels}
			byTarget[key] = current
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

	var derived []Sample
	for _, current := range byTarget {
		if current.total == nil && current.used != nil && current.free != nil {
			value := *current.used + *current.free
			current.total = &value
			derived = appendUnique(derived, existing, Sample{
				Name: "accelerator_memory_total_bytes", Labels: cloneLabels(current.labels), Value: value,
			})
		}
		if current.free == nil && current.total != nil && current.used != nil {
			value := *current.total - *current.used
			if value < 0 {
				value = 0
			}
			derived = appendUnique(derived, existing, Sample{
				Name: "accelerator_memory_free_bytes", Labels: cloneLabels(current.labels), Value: value,
			})
		}
		if current.used != nil && current.total != nil && *current.total > 0 {
			value := *current.used / *current.total * 100
			derived = appendUnique(derived, existing, Sample{
				Name: "accelerator_memory_utilization_percentage", Labels: cloneLabels(current.labels), Value: value,
			})
		}
	}
	return derived
}

// deriveInfoMetrics produces the observation target inventory. accelerator_info
// is the only source of the inventory, so it is emitted for every target the
// scrape revealed, including targets whose only samples were derived.
func deriveInfoMetrics(identities map[string]Identity) []Sample {
	result := make([]Sample, 0, len(identities))
	for _, identity := range identities {
		result = append(result, Sample{
			Name: InventoryMetricName, Labels: identity.infoLabels(), Value: 1,
		})
	}
	return result
}

// deriveHealthState fans the diagnostics of one observation target into exactly
// one health state.
//
// sources names the native families that constitute a health source for the
// rule. When none of them was scraped the target has no health source at all
// and no state is produced, so a consumer can tell "healthy" from "not
// reported" instead of reading a missing series as healthy.
//
// The state is derived from the target set the rule observed rather than from
// the diagnostics alone: a device whose every health check passes produces no
// diagnostic, and it is exactly that case which must resolve to healthy.
func deriveHealthState(sources ...string) Deriver {
	return func(raw Families, converted []Sample, identities map[string]Identity) []Sample {
		reported := false
		for _, source := range sources {
			if len(raw[source]) > 0 {
				reported = true
				break
			}
		}
		if !reported {
			return nil
		}

		states := map[string]string{}
		for key := range identities {
			states[key] = HealthStateHealthy
		}
		for _, sample := range converted {
			if sample.Name != "accelerator_health_diagnostic_info" {
				continue
			}
			key := identityFromLabels(sample.Labels).Key()
			states[key] = worseHealthState(states[key], stateFromSeverity(sample.Labels["severity"]))
		}

		result := make([]Sample, 0, len(identities))
		for key, identity := range identities {
			labels := identity.deviceLabels()
			labels["state"] = states[key]
			result = append(result, Sample{Name: "accelerator_health_state", Labels: labels, Value: 1})
		}
		return result
	}
}

var healthStateRank = map[string]int{
	HealthStateHealthy:  1,
	HealthStateDegraded: 2,
	HealthStateFault:    3,
}

func worseHealthState(current, candidate string) string {
	if healthStateRank[candidate] > healthStateRank[current] {
		return candidate
	}
	return current
}

// stateFromSeverity escalates a diagnostic severity into a health state. An
// informational diagnostic does not make a device unhealthy.
func stateFromSeverity(severity string) string {
	switch severity {
	case SeverityCritical:
		return HealthStateFault
	case SeverityWarning:
		return HealthStateDegraded
	default:
		return HealthStateHealthy
	}
}

func sampleKeys(samples []Sample) map[string]struct{} {
	keys := make(map[string]struct{}, len(samples))
	for _, sample := range samples {
		keys[sampleKey(sample)] = struct{}{}
	}
	return keys
}

func appendUnique(samples []Sample, existing map[string]struct{}, sample Sample) []Sample {
	key := sampleKey(sample)
	if _, ok := existing[key]; ok {
		return samples
	}
	existing[key] = struct{}{}
	return append(samples, sample)
}
