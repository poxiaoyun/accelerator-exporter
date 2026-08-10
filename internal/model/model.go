// Package model contains the internal data exchanged by the scraper,
// transformers, and exporter.
package model

// Supported accelerator source types.
const (
	AcceleratorTypeGPU  = "gpu"
	AcceleratorTypeNPU  = "npu"
	AcceleratorTypeVGPU = "vgpu"
	AcceleratorTypeVNPU = "vnpu"
)

// Source describes one directly scraped metrics endpoint.
type Source struct {
	Name            string
	Address         string
	AcceleratorType string
	Labels          map[string]string
}

// RawSample is one sample parsed from a source metrics endpoint.
type RawSample struct {
	Labels map[string]string
	Value  float64
}

// Families groups source samples by their original metric name.
type Families map[string][]RawSample

// Sample is one normalized metric exposed by accelerator-exporter.
type Sample struct {
	Name   string
	Labels map[string]string
	Value  float64
}
