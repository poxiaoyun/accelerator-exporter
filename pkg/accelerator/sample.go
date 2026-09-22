package accelerator

// Source describes one directly scraped metrics endpoint.
type Source struct {
	// Name is the discovery-assigned target name.
	Name string
	// Address is the metrics endpoint URL.
	Address string
	// Rule is the built-in rule name that interprets this source.
	Rule string
	// Labels are configured fixed labels. They never override the identity,
	// classification or position groups resolved from the samples.
	Labels map[string]string
}

// RawSample is one sample parsed from a source metrics endpoint.
type RawSample struct {
	Labels map[string]string
	Value  float64
}

// Families groups native source samples by their original metric name.
type Families map[string][]RawSample

// Sample is one normalized sample exposed by accelerator-exporter.
type Sample struct {
	Name   string
	Labels map[string]string
	Value  float64
}
