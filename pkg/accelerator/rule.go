package accelerator

import (
	"sort"
	"strings"
)

// Inventory maps a native physical device identifier onto the metadata that
// only some metric families of the source carry.
type Inventory map[string]DeviceInfo

// DeviceInfo is physical device metadata used to complete virtual instance
// samples whose own metric family omits node and model.
type DeviceInfo struct {
	Node  string
	Model string
}

// InventoryInput is one successful source scrape offered to BuildInventory.
type InventoryInput struct {
	Source   Source
	Families Families
}

// Environment is everything a Rule may look at for one scrape besides the
// samples themselves.
//
// It is one value rather than a growing parameter list because the inputs a
// rule needs do not all come from sample labels: the vGPU slice identifier is
// only in the device allocation fact. Every rule receives the same value, so a
// future allocation source (a vNPU parent, a MIG device identifier) needs no
// signature change.
type Environment struct {
	// Source is the configured scrape source the samples came from.
	Source Source
	// Inventory is the physical device metadata the round revealed, used to
	// complete instance samples whose own metric family omits node and model.
	Inventory Inventory
	// Allocations are the device allocation facts of the node this round.
	Allocations []Allocation
}

// Metric maps one native metric onto one contract metric.
type Metric struct {
	// Source is the native metric name. Exactly one of Source and Sum is set.
	Source string
	// Sum lists several native metrics whose values are added per observation
	// target to produce one contract sample, for a reading the vendor only
	// publishes in parts. Every listed metric must be present, otherwise nothing
	// is produced and the metric falls back to derivation. Factor applies to the
	// sum; Encode and Emit do not apply to a sum.
	Sum []string
	// Canonical is the contract metric name.
	Canonical string
	// Factor converts the native unit into the contract unit. A rule whose
	// native unit already equals the contract unit uses 1.
	Factor float64
	// Encode converts the native value convention, for example a health value
	// where zero means healthy. Nil means the value is used as-is.
	Encode func(float64) float64
	// Emit selects which native samples produce a contract sample. Nil means
	// every sample does.
	Emit func(float64) bool
	// Enums are constant enum labels of this mapping.
	Enums map[string]string
	// EnumsFrom copy enum labels from the native sample.
	EnumsFrom []EnumAlias
}

// EnumAlias declares one contract enum label and the native label names it can
// come from. Values are lower-cased and sanitized to satisfy the label value
// rules.
type EnumAlias struct {
	Label   string
	Aliases []string
	// Map translates the sanitized native value into the contract's closed
	// value set. A native value with no entry produces no label at all, so the
	// sample is dropped by contract validation instead of exposing a value the
	// contract does not declare.
	Map map[string]string
}

// Deriver appends samples that a one-to-one mapping cannot express. It sees the
// native families of the source, the samples the mapping already produced, and
// every observation target the source revealed.
type Deriver func(raw Families, converted []Sample, identities map[string]Identity) []Sample

// Target is one observation target class a Rule can produce.
//
// For example npu-exporter exposes both physical NPU and vNPU metric families,
// so its Rule has two Targets and the same endpoint is scraped once.
type Target struct {
	// Identify resolves the observation target identity from one native sample.
	// It may return an incomplete identity: Apply keeps the measurement and
	// leaves the unresolved labels out, and contract validation reports the gap
	// as a violation rather than dropping the sample.
	Identify func(env Environment, raw map[string]string) Identity
	// Metrics are the native metric mappings of this target class.
	Metrics []Metric
	// Derive appends samples that cannot be expressed as a one-to-one mapping,
	// for example fanning several vendor health sources into a single health
	// state. Vendor semantics stay in the vendor file.
	Derive []Deriver
	// NativeResources declare the extended resources this target class is
	// allocated under, which is what lets the relation layer resolve an
	// allocation fact to one of these targets.
	NativeResources []NativeResource
}

// Rule is a stateless interpretation of one scrape source. It owns the source's
// vendor, the observation target classes it can produce, and their metric
// mappings. One rule file per vendor.
type Rule struct {
	// Name is the name a source configuration references.
	Name string
	// Vendor is the vendor of every observation target of the source.
	Vendor string
	// Targets are the observation target classes the source can produce.
	Targets []Target
}

var rules = []Rule{
	nvidiaDCGMRule,
	ascendNPURule,
	volcanoVGPURule,
}

// RuleByName resolves a configured reference to a built-in Rule.
func RuleByName(name string) (Rule, bool) {
	for _, rule := range rules {
		if rule.Name == name {
			return rule, true
		}
	}
	return Rule{}, false
}

// RuleNames returns every built-in Rule name in stable order.
func RuleNames() []string {
	names := make([]string, 0, len(rules))
	for _, rule := range rules {
		names = append(names, rule.Name)
	}
	sort.Strings(names)
	return names
}

// Result is the outcome of normalizing one source scrape.
type Result struct {
	// Samples are the device-layer samples of the source.
	Samples []Sample
	// Identities are the observation targets the scrape revealed, keyed by
	// Identity.Key.
	Identities map[string]Identity
	// AllocationIndex resolves one allocation fact to an observation target,
	// keyed by allocationKey.
	AllocationIndex map[string]Identity
}

// Apply normalizes one source scrape into device-layer samples.
//
// It is a method because interpreting a source is what a Rule is: the identity
// construction, the mappings and the derivations are all the rule's own, and a
// free function taking the rule as its first argument would leave Rule a data
// bag that something else has to understand.
//
// A sample is reported as long as the source produced it. An incomplete
// identity is not a reason to drop a measurement: the labels that could not be
// resolved are omitted, contract validation reports the gap, and the sample
// still contributes to whatever aggregation it can support.
func (rule Rule) Apply(env Environment, families Families) Result {
	result := Result{
		Identities:      map[string]Identity{},
		AllocationIndex: map[string]Identity{},
	}
	for _, target := range rule.Targets {
		converted, identities := applyTarget(target, env, families, result.AllocationIndex)
		result.Samples = append(result.Samples, converted...)
		for key, identity := range identities {
			result.Identities[key] = mergeIdentity(result.Identities[key], identity)
		}
	}
	result.Samples = append(result.Samples, deriveMemoryMetrics(result.Samples)...)
	result.Samples = append(result.Samples, deriveInfoMetrics(result.Identities)...)
	sort.SliceStable(result.Samples, func(i, j int) bool {
		return sampleKey(result.Samples[i]) < sampleKey(result.Samples[j])
	})
	return result
}

func applyTarget(
	target Target, env Environment, families Families, allocationIndex map[string]Identity,
) ([]Sample, map[string]Identity) {
	var samples []Sample
	identities := map[string]Identity{}

	record := func(identity Identity, raw map[string]string) {
		key := identity.Key()
		identities[key] = mergeIdentity(identities[key], identity)
		for _, native := range target.NativeResources {
			if native.Mode != identity.DeviceMode {
				continue
			}
			nativeDeviceID := native.DeviceID(env, raw)
			if nativeDeviceID == "" {
				continue
			}
			for _, nativeResource := range native.Names {
				allocationIndex[allocationKey(nativeResource, nativeDeviceID)] = identity
			}
		}
	}

	// Canonical pass-through: a source that already exposes contract metric
	// names keeps them, once the identity and classification groups are filled
	// in. Only the metric's declared enum labels survive, so a vendor label can
	// never reach the public contract.
	//
	// The two exporter-owned metrics are skipped: accelerator_info is derived
	// from the resolved identity so it can carry the static information the rest
	// of the device layer does not, and accelerator_pod_info comes from the
	// device allocation fact rather than from a scrape.
	for _, name := range CanonicalMetricNames() {
		if name == InventoryMetricName || name == PodInfoMetricName {
			continue
		}
		if len(families[name]) == 0 {
			continue
		}
		metadata, _ := CanonicalMetric(name)
		for _, raw := range families[name] {
			identity := resolveIdentity(target, raw, env)
			if !appliesTo(metadata, identity.DeviceMode) {
				continue
			}
			labels := identity.deviceLabels()
			for _, enum := range metadata.Enums {
				setIfPresent(labels, enum, labelValue(raw.Labels[enum]))
			}
			record(identity, raw.Labels)
			samples = append(samples, Sample{Name: name, Labels: labels, Value: contractValue(metadata, raw.Value)})
		}
	}

	for _, mapping := range target.Metrics {
		metadata, _ := CanonicalMetric(mapping.Canonical)
		for _, raw := range families[mapping.Source] {
			identity := resolveIdentity(target, raw, env)
			// The target is recorded before the mapping decides whether this
			// particular row is a measurement it can express. A device that is
			// observed at all belongs in the inventory, and a source that only
			// reports healthy rows must still resolve to a target.
			record(identity, raw.Labels)
			if !appliesTo(metadata, identity.DeviceMode) {
				continue
			}
			if mapping.Emit != nil && !mapping.Emit(raw.Value) {
				continue
			}
			labels := identity.deviceLabels()
			for name, value := range mapping.Enums {
				labels[name] = value
			}
			for _, enum := range mapping.EnumsFrom {
				value := labelValue(first(raw.Labels, enum.Aliases...))
				if len(enum.Map) > 0 {
					value = enum.Map[value]
				}
				setIfPresent(labels, enum.Label, value)
			}
			value := raw.Value * mapping.Factor
			if mapping.Encode != nil {
				value = mapping.Encode(raw.Value)
			}
			samples = append(samples, Sample{Name: mapping.Canonical, Labels: labels, Value: contractValue(metadata, value)})
		}
	}

	for _, mapping := range target.Metrics {
		if len(mapping.Sum) == 0 {
			continue
		}
		converted, ok := sumMapping(target, mapping, env, families, record)
		if ok {
			samples = append(samples, converted...)
		}
	}

	for _, derive := range target.Derive {
		samples = append(samples, derive(families, samples, identities)...)
	}
	return samples, identities
}

// sumMapping adds several native metrics together per observation target, for a
// reading the vendor only publishes in parts.
//
// It reports false when any of the listed metrics is missing, because a partial
// sum is a different number and silently exposing it would understate the
// reading. The caller then leaves the metric to derivation.
func sumMapping(
	target Target, mapping Metric, env Environment, families Families,
	record func(Identity, map[string]string),
) ([]Sample, bool) {
	metadata, _ := CanonicalMetric(mapping.Canonical)
	totals := map[string]float64{}
	labels := map[string]map[string]string{}
	for _, name := range mapping.Sum {
		family := families[name]
		if len(family) == 0 {
			return nil, false
		}
		for _, raw := range family {
			identity := resolveIdentity(target, raw, env)
			record(identity, raw.Labels)
			key := identity.Key()
			totals[key] += raw.Value
			labels[key] = identity.deviceLabels()
		}
	}

	converted := make([]Sample, 0, len(totals))
	for key, total := range totals {
		if !appliesTo(metadata, labels[key][LabelDeviceMode]) {
			continue
		}
		converted = append(converted, Sample{
			Name:   mapping.Canonical,
			Labels: labels[key],
			Value:  contractValue(metadata, total*mapping.Factor),
		})
	}
	return converted, true
}

// appliesTo reports whether an observation target's device mode is within the
// modes the contract declares for a metric.
//
// This is where the specification rule that a partition or a virtual instance
// has no independent temperature, power, hardware error or throttle reason is
// enforced: the mapping is not performed, so the series simply does not exist
// and a consumer reads the absence as missing data.
func appliesTo(metadata MetricMetadata, deviceMode string) bool {
	if len(metadata.Modes) == 0 {
		return true
	}
	if deviceMode == "" {
		// The mode could not be resolved, so the restriction cannot be applied
		// here. Contract validation reports the identity gap instead.
		return true
	}
	return contains(metadata.Modes, deviceMode)
}

// resolveIdentity normalizes the identity of one native sample.
//
// It never fails. A source that cannot name its observation target still has a
// measurement worth reporting, so the unresolved labels are left out and
// contract validation reports the gap against the contract instead.
func resolveIdentity(target Target, raw RawSample, env Environment) Identity {
	return target.Identify(env, raw.Labels).normalize()
}

// contractValue applies the value the contract fixes for a metric, overriding
// whatever the vendor reading was. A rule declares which series exist; the
// contract declares what a series means.
func contractValue(metadata MetricMetadata, value float64) float64 {
	if metadata.Info {
		return 1
	}
	return value
}

// mergeIdentity fills the empty fields of an already registered identity from a
// later observation, so static information carried by only one metric family
// still reaches accelerator_info.
func mergeIdentity(existing, incoming Identity) Identity {
	merged := existing
	for _, field := range []struct {
		target *string
		source string
	}{
		{&merged.Node, incoming.Node},
		{&merged.DeviceID, incoming.DeviceID},
		{&merged.ParentDeviceID, incoming.ParentDeviceID},
		{&merged.DeviceType, incoming.DeviceType},
		{&merged.DeviceMode, incoming.DeviceMode},
		{&merged.Vendor, incoming.Vendor},
		{&merged.Model, incoming.Model},
		{&merged.DeviceIndex, incoming.DeviceIndex},
		{&merged.Profile, incoming.Profile},
		{&merged.DriverVersion, incoming.DriverVersion},
		{&merged.FirmwareVersion, incoming.FirmwareVersion},
		{&merged.SerialNumber, incoming.SerialNumber},
		{&merged.PCIBusID, incoming.PCIBusID},
		{&merged.Architecture, incoming.Architecture},
	} {
		if *field.target == "" {
			*field.target = field.source
		}
	}
	return merged
}

// zeroIsHealthy converts a native health gauge where zero means healthy.
func zeroIsHealthy(value float64) float64 {
	if value == 0 {
		return 1
	}
	return 0
}

// positiveAsOne reports a value as one when it is observed at all.
func positiveAsOne(value float64) float64 {
	if value > 0 {
		return 1
	}
	return 0
}

// nonzero selects samples whose value indicates an observed condition.
func nonzero(value float64) bool {
	return value != 0
}

// zeroValue selects samples whose value indicates an observed condition in a
// source where a nonzero value means healthy.
func zeroValue(value float64) bool {
	return value == 0
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

func sampleKey(sample Sample) string {
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
