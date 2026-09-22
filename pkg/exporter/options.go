package exporter

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"xiaoshiai.cn/accelerator-exporter/pkg/scrapetargets"
)

// DefaultPodResourcesSocket is where kubelet exposes the device allocation facts
// that accelerator_pod_info is built from.
const DefaultPodResourcesSocket = "/var/lib/kubelet/pod-resources/kubelet.sock"

// Options is the structural configuration of the accelerator-exporter process.
//
// It carries only serializable values, so it has the same shape whether it comes
// from the configuration file, a default or a test. The external systems the
// exporter reads are deliberately absent: Run initializes Options into the
// package-internal configuration that holds the resolved instances, so an
// instance can never end up in a configuration file.
type Options struct {
	// ListenAddress serves /metrics, /healthz and /readyz.
	ListenAddress string `yaml:"listenAddress" description:"Metrics and probe listen address"`
	// ScrapeInterval is the period between collection rounds.
	ScrapeInterval time.Duration `yaml:"scrapeInterval" description:"Period between accelerator metrics collection rounds"`
	// ScrapeTimeout bounds one source scrape.
	ScrapeTimeout time.Duration `yaml:"scrapeTimeout" description:"Timeout for one accelerator metrics source scrape"`
	// MaxConcurrency bounds how many sources are scraped at once.
	MaxConcurrency int `yaml:"maxConcurrency" description:"Maximum concurrent accelerator metrics source scrapes"`
	// NodeName is the Node this process collects. Empty falls back to the
	// NODE_NAME environment variable the DaemonSet injects.
	NodeName string `yaml:"nodeName,omitempty" description:"Node name; defaults to the NODE_NAME environment variable"`
	// Kubeconfig selects an out-of-cluster Kubernetes configuration. Empty uses
	// the in-cluster ServiceAccount.
	Kubeconfig string `yaml:"kubeconfig,omitempty" description:"Kubeconfig path for out-of-cluster runs"`
	// PodResourcesSocket is kubelet's pod-resources socket.
	PodResourcesSocket string `yaml:"podResourcesSocket,omitempty" description:"kubelet pod-resources socket path"`
	// Sources are the accelerator metrics sources to collect.
	Sources []scrapetargets.Source `yaml:"sources" description:"Accelerator metrics sources"`
}

// NewDefaultOptions returns the defaults of a standard kubeadm node, whose
// kubelet keeps everything under /var/lib/kubelet.
func NewDefaultOptions() *Options {
	return &Options{
		ListenAddress:      ":9400",
		ScrapeInterval:     15 * time.Second,
		ScrapeTimeout:      5 * time.Second,
		MaxConcurrency:     8,
		PodResourcesSocket: DefaultPodResourcesSocket,
	}
}

// LoadOptions reads, defaults and validates a configuration file.
//
// Unknown fields are rejected rather than ignored: a misspelled key quietly
// taking its default is how a collector ends up scraping nothing while
// reporting itself ready.
func LoadOptions(path string) (*Options, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("a configuration file is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}
	options := NewDefaultOptions()
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(options); err != nil {
		return nil, fmt.Errorf("decode config %q: %w", path, err)
	}
	if err := options.Validate(); err != nil {
		return nil, fmt.Errorf("config %q: %w", path, err)
	}
	return options, nil
}

// Validate reports whether the options can start the process.
//
// It validates the whole configuration, including the sources. New validates
// only the collection loop, because it is handed dependencies that are already
// built and has no use for the source definitions.
func (options *Options) Validate() error {
	if options == nil {
		return fmt.Errorf("exporter options are required")
	}
	if err := validateCollection(options.ScrapeInterval, options.ScrapeTimeout, options.MaxConcurrency); err != nil {
		return err
	}
	if len(options.Sources) == 0 {
		return fmt.Errorf("at least one source must be configured")
	}
	names := map[string]struct{}{}
	for index, source := range options.Sources {
		if err := source.Validate(); err != nil {
			return fmt.Errorf("sources[%d]: %w", index, err)
		}
		if _, exists := names[source.Name]; exists {
			return fmt.Errorf("duplicate source name %q", source.Name)
		}
		names[source.Name] = struct{}{}
	}
	return nil
}

// validateCollection checks the settings the collection loop itself needs.
func validateCollection(interval, timeout time.Duration, concurrency int) error {
	if interval <= 0 {
		return fmt.Errorf("scrapeInterval must be positive")
	}
	if timeout <= 0 {
		return fmt.Errorf("scrapeTimeout must be positive")
	}
	if concurrency < 1 {
		return fmt.Errorf("maxConcurrency must be positive")
	}
	return nil
}
