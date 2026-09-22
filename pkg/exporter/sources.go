package exporter

import (
	"context"

	"xiaoshiai.cn/accelerator-exporter/pkg/accelerator"
)

// The exporter reads three external systems. Each answers a different question
// and is injected separately, so the collection loop can be exercised without
// Kubernetes, without HTTP and without a kubelet, and so this package never
// imports a client library.
//
//	Scraper            — reads the metrics one named scrape target exposes
//	TargetProvider     — resolves which named scrape targets this node has
//	AllocationProvider — reads which Pod owns which observation target
//
// A scrape target is always the whole accelerator.Source, never a bare address,
// so its configured name travels with it and an implementation can report which
// source it failed on.
//
// A nil AllocationProvider is valid: it means the node has no device allocation
// source, in which case the exporter still produces the whole device layer but
// no relation layer and therefore no workload attribution.

// Scraper reads and parses the native metrics of one scrape target.
type Scraper interface {
	Scrape(ctx context.Context, target accelerator.Source) (accelerator.Families, error)
}

// TargetProvider resolves the accelerator metrics targets of one node. Each
// returned target carries the name of the source configuration it came from.
//
// It may return targets alongside an error: a discovery provider that failed for
// one namespace must not hide the targets it did resolve.
type TargetProvider interface {
	Targets(ctx context.Context) ([]accelerator.Source, error)
}

// AllocationProvider reads the device allocation facts of one node, which are
// the authority on which Pod owns which observation target.
//
// It may return facts alongside an error, for the same reason TargetProvider
// may.
type AllocationProvider interface {
	Allocations(ctx context.Context) ([]accelerator.Allocation, error)
}
