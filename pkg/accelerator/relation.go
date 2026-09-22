package accelerator

// This file holds the relation layer. It joins the device allocation facts of a
// node with the observation targets a scrape revealed.
//
// The authority on ownership is the allocation fact, never a workload label a
// vendor exporter happened to attach to a metric. When an allocation cannot be
// resolved to a real observation target nothing is produced for it, because a
// sentinel would put a non-existent device into every query that filters on
// device_id.

// Allocation is one device allocation fact read from the allocation source. It
// carries no observation value, only who was given which target.
type Allocation struct {
	Namespace string
	Pod       string
	PodUID    string
	Container string
	// NativeResource is the Kubernetes extended resource name the allocation
	// came from.
	NativeResource string
	// NativeDeviceID is the identifier the device plugin reported for the
	// allocated device.
	NativeDeviceID string
}

// NativeResource declares how one set of native Kubernetes extended resources
// maps onto the observation targets of a Target.
type NativeResource struct {
	// Names are the extended resource names this declaration covers. Several
	// names are listed when a vendor publishes the same device class under more
	// than one resource name.
	Names []string
	// Mode is the device mode of the targets allocated under these resources. It
	// keeps a whole-device allocation from resolving onto a partition or a
	// virtual instance that happens to report the same native identifier.
	Mode string
	// DeviceID reads the identifier the device plugin reports for the
	// observation target of one native sample. It must agree with the
	// identifiers that appear in the allocation facts, otherwise the relation
	// layer resolves nothing and reports nothing. It takes the whole
	// Environment because the identifier is not always a sample label: for a
	// vGPU slice the allocation fact is the only source of it.
	DeviceID func(env Environment, raw map[string]string) string
}

// allocationKey is the lookup key of the allocation index.
func allocationKey(nativeResource, nativeDeviceID string) string {
	return nativeResource + "\xff" + nativeDeviceID
}

// BuildPodInfo joins the allocation facts of the node with the observation
// targets every source revealed, and produces the relation layer.
//
// indexes are the per-source allocation indexes returned by Apply. Allocations
// that resolve to no observation target are skipped, and so are allocations that
// do not name a Pod UID: the specification makes pod_uid mandatory because two
// Pods with the same name are different allocation relationships.
func BuildPodInfo(allocations []Allocation, indexes ...map[string]Identity) []Sample {
	if len(allocations) == 0 {
		return nil
	}
	resolved := map[string]Identity{}
	for _, index := range indexes {
		for key, identity := range index {
			// First wins, as everywhere else an allocation or a series can be
			// reached through more than one source: the provider order is the
			// configured order of the sources, so letting a later source
			// overwrite would make the outcome depend on which sources happen to
			// be enabled. Today every declaration of one resource resolves to the
			// same node and device_id, so this only removes the surprise.
			if _, exists := resolved[key]; exists {
				continue
			}
			resolved[key] = identity
		}
	}

	samples := make([]Sample, 0, len(allocations))
	seen := map[string]struct{}{}
	for _, allocation := range allocations {
		if allocation.Namespace == "" || allocation.Pod == "" || allocation.Container == "" || allocation.PodUID == "" {
			continue
		}
		identity, ok := resolved[allocationKey(allocation.NativeResource, allocation.NativeDeviceID)]
		if !ok {
			continue
		}
		labels := map[string]string{
			LabelNode:      identity.Node,
			LabelDeviceID:  identity.DeviceID,
			LabelNamespace: allocation.Namespace,
			LabelPod:       allocation.Pod,
			LabelPodUID:    allocation.PodUID,
			LabelContainer: allocation.Container,
		}
		sample := Sample{Name: PodInfoMetricName, Labels: labels, Value: 1}
		key := sampleKey(sample)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		samples = append(samples, sample)
	}
	return samples
}
