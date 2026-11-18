package accelerator

// BuildInventory extracts physical GPU identity from successful source scrapes.
// Virtual instance samples inherit node and model from the physical device that
// hosts them, because their own metric families omit those labels.
func BuildInventory(inputs []InventoryInput) Inventory {
	inventory := Inventory{}
	for _, input := range inputs {
		if input.Source.Rule == RuleIluvatarIX {
			buildIluvatarInventory(inventory, input)
			continue
		}
		if input.Source.Rule != RuleNvidiaDCGM {
			continue
		}
		for _, samples := range input.Families {
			for _, sample := range samples {
				uuid := first(sample.Labels, "UUID", "uuid")
				if uuid == "" {
					continue
				}
				current := inventory[uuid]
				current.Node = firstNonEmpty(current.Node, first(sample.Labels, "node", "Hostname", "hostname"), input.Source.Labels[LabelNode])
				current.Model = firstNonEmpty(current.Model, first(sample.Labels, "model", "modelName", "model_name"), input.Source.Labels[LabelModel])
				inventory[uuid] = current
			}
		}
	}
	return inventory
}
