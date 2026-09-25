package accelerator

import "maps"

// Identity is the resolved identity, classification, position and static
// information of one observation target.
//
// A Rule resolves it from one native sample. Identity is the only type that
// knows the label names of the device layer, so Identity.deviceLabels is the
// single emission point for them.
type Identity struct {
	// Identity group.
	Node           string
	DeviceID       string
	ParentDeviceID string

	// Classification group.
	DeviceType string
	DeviceMode string
	Vendor     string
	Model      string

	// Position group. Meaningful only for DeviceModePartition.
	DeviceIndex string

	// Static information. Carried only by accelerator_info.
	Profile         string
	DriverVersion   string
	FirmwareVersion string
	SerialNumber    string
	PCIBusID        string
	Architecture    string
}

// normalize fills the mandatory classification fallbacks and removes position
// labels that do not apply to the target's mode.
func (identity Identity) normalize() Identity {
	if identity.Model == "" {
		identity.Model = ModelUnknown
	}
	if identity.Vendor == "" {
		identity.Vendor = VendorUnknown
	}
	if identity.DeviceMode != DeviceModePartition {
		identity.DeviceIndex = ""
	}
	if identity.DeviceMode == DeviceModePhysical {
		identity.ParentDeviceID = ""
	}
	return identity
}

// Key is the observation target key. (node, device_id) is the only unique key
// of a target; device_id and parent_device_id are never compared across modes.
func (identity Identity) Key() string {
	return identity.Node + "\xff" + identity.DeviceID
}

// deviceLabels returns the label set of a device-layer metric: the identity,
// classification and position groups, and nothing else. In particular it never
// carries an ownership label such as namespace, pod, container or instance.
func (identity Identity) deviceLabels() map[string]string {
	labels := map[string]string{}
	setIfPresent(labels, LabelNode, identity.Node)
	setIfPresent(labels, LabelDeviceID, identity.DeviceID)
	setIfPresent(labels, LabelParentDeviceID, identity.ParentDeviceID)
	setIfPresent(labels, LabelDeviceType, identity.DeviceType)
	setIfPresent(labels, LabelDeviceMode, identity.DeviceMode)
	setIfPresent(labels, LabelVendor, identity.Vendor)
	setIfPresent(labels, LabelModel, identity.Model)
	setIfPresent(labels, LabelDeviceIndex, identity.DeviceIndex)
	return labels
}

// infoLabels returns the device-layer labels plus the static information. It is
// used only for the inventory metric.
func (identity Identity) infoLabels() map[string]string {
	labels := identity.deviceLabels()
	setIfPresent(labels, LabelProfile, identity.Profile)
	setIfPresent(labels, LabelDriverVersion, identity.DriverVersion)
	setIfPresent(labels, LabelFirmwareVersion, identity.FirmwareVersion)
	setIfPresent(labels, LabelSerialNumber, identity.SerialNumber)
	setIfPresent(labels, LabelPCIBusID, identity.PCIBusID)
	setIfPresent(labels, LabelArchitecture, identity.Architecture)
	return labels
}

// identityFromLabels rebuilds the identity and position groups from a device
// layer sample, so derivation and the relation layer can key on a target
// without carrying a separate structure.
func identityFromLabels(labels map[string]string) Identity {
	return Identity{
		Node:           labels[LabelNode],
		DeviceID:       labels[LabelDeviceID],
		ParentDeviceID: labels[LabelParentDeviceID],
		DeviceType:     labels[LabelDeviceType],
		DeviceMode:     labels[LabelDeviceMode],
		Vendor:         labels[LabelVendor],
		Model:          labels[LabelModel],
		DeviceIndex:    labels[LabelDeviceIndex],
	}
}

// setIfPresent sets a label only when the value is not empty, so an unresolved
// attribute is omitted instead of exposed as an empty label value.
func setIfPresent(labels map[string]string, name, value string) {
	if value != "" {
		labels[name] = value
	}
}

func cloneLabels(labels map[string]string) map[string]string {
	cloned := make(map[string]string, len(labels))
	maps.Copy(cloned, labels)
	return cloned
}
