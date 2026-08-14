// Package config loads accelerator-exporter runtime configuration.
package config

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	"xiaoshiai.cn/rune/internal/discovery"
	"xiaoshiai.cn/rune/internal/model"
)

var prometheusLabelName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// Runtime is validated configuration used by main.
type Runtime struct {
	ListenAddress   string
	ScrapeInterval  time.Duration
	ScrapeTimeout   time.Duration
	MaxConcurrency  int
	PodMonitors     []discovery.PodMonitor
	ServiceMonitors []discovery.ServiceMonitor
	Kubeconfig      string
}

type fileConfig struct {
	ListenAddress  string       `yaml:"listenAddress"`
	ScrapeInterval string       `yaml:"scrapeInterval"`
	ScrapeTimeout  string       `yaml:"scrapeTimeout"`
	MaxConcurrency int          `yaml:"maxConcurrency"`
	Kubeconfig     string       `yaml:"kubeconfig"`
	Sources        []sourceFile `yaml:"sources"`
}

type sourceFile struct {
	Name            string            `yaml:"name"`
	AcceleratorType string            `yaml:"acceleratorType"`
	Labels          map[string]string `yaml:"labels"`
	PodMonitor      *monitorFile      `yaml:"podMonitor"`
	ServiceMonitor  *monitorFile      `yaml:"serviceMonitor"`
}

type monitorFile struct {
	Namespaces        []string              `yaml:"namespaces"`
	NamespaceSelector namespaceSelectorFile `yaml:"namespaceSelector"`
	Selector          labelSelectorFile     `yaml:"selector"`
	Port              portValue             `yaml:"port"`
	Path              string                `yaml:"path"`
	Scheme            string                `yaml:"scheme"`
}

type namespaceSelectorFile struct {
	Any        bool     `yaml:"any"`
	MatchNames []string `yaml:"matchNames"`
}

type labelSelectorFile struct {
	MatchLabels      map[string]string          `yaml:"matchLabels"`
	MatchExpressions []labelSelectorRequirement `yaml:"matchExpressions"`
}

type labelSelectorRequirement struct {
	Key      string   `yaml:"key"`
	Operator string   `yaml:"operator"`
	Values   []string `yaml:"values"`
}

type portValue string

func (value *portValue) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode || (node.Tag != "!!str" && node.Tag != "!!int") {
		return fmt.Errorf("port must be a name or integer")
	}
	*value = portValue(node.Value)
	return nil
}

// Load reads and validates a YAML configuration file.
func Load(path string) (Runtime, error) {
	file, err := os.Open(path)
	if err != nil {
		return Runtime{}, fmt.Errorf("open config %q: %w", path, err)
	}
	defer file.Close()
	return Decode(file)
}

// Decode reads and validates YAML configuration.
func Decode(reader io.Reader) (Runtime, error) {
	var file fileConfig
	decoder := yaml.NewDecoder(reader)
	decoder.KnownFields(true)
	if err := decoder.Decode(&file); err != nil {
		return Runtime{}, fmt.Errorf("decode config: %w", err)
	}
	if file.ListenAddress == "" {
		file.ListenAddress = ":9400"
	}
	if file.ScrapeInterval == "" {
		file.ScrapeInterval = "15s"
	}
	if file.ScrapeTimeout == "" {
		file.ScrapeTimeout = "5s"
	}
	if file.MaxConcurrency == 0 {
		file.MaxConcurrency = 8
	}
	interval, err := positiveDuration("scrapeInterval", file.ScrapeInterval)
	if err != nil {
		return Runtime{}, err
	}
	timeout, err := positiveDuration("scrapeTimeout", file.ScrapeTimeout)
	if err != nil {
		return Runtime{}, err
	}
	if file.MaxConcurrency < 1 {
		return Runtime{}, fmt.Errorf("maxConcurrency must be positive")
	}
	if len(file.Sources) == 0 {
		return Runtime{}, fmt.Errorf("at least one source must be configured")
	}

	runtime := Runtime{
		ListenAddress: file.ListenAddress, ScrapeInterval: interval, ScrapeTimeout: timeout, MaxConcurrency: file.MaxConcurrency,
		Kubeconfig: strings.TrimSpace(file.Kubeconfig),
	}
	names := map[string]struct{}{}
	for index, configured := range file.Sources {
		configured.Name = strings.TrimSpace(configured.Name)
		configured.AcceleratorType = strings.ToLower(strings.TrimSpace(configured.AcceleratorType))
		if configured.Name == "" {
			return Runtime{}, fmt.Errorf("sources[%d].name is required", index)
		}
		if _, exists := names[configured.Name]; exists {
			return Runtime{}, fmt.Errorf("duplicate source name %q", configured.Name)
		}
		names[configured.Name] = struct{}{}
		if !supportedType(configured.AcceleratorType) {
			return Runtime{}, fmt.Errorf("source %q has unsupported acceleratorType %q", configured.Name, configured.AcceleratorType)
		}
		labels := make(map[string]string, len(configured.Labels))
		for name, value := range configured.Labels {
			if !prometheusLabelName.MatchString(name) {
				return Runtime{}, fmt.Errorf("source %q has invalid label name %q", configured.Name, name)
			}
			if name == "accelerator_type" {
				return Runtime{}, fmt.Errorf("source %q must configure acceleratorType instead of accelerator_type label", configured.Name)
			}
			if value != "" {
				labels[name] = value
			}
		}
		modes := 0
		if configured.PodMonitor != nil {
			modes++
		}
		if configured.ServiceMonitor != nil {
			modes++
		}
		if modes != 1 {
			return Runtime{}, fmt.Errorf("source %q must configure exactly one of podMonitor or serviceMonitor", configured.Name)
		}
		if configured.PodMonitor != nil {
			monitor, err := buildPodMonitor(configured.Name, configured.AcceleratorType, labels, configured.PodMonitor)
			if err != nil {
				return Runtime{}, err
			}
			runtime.PodMonitors = append(runtime.PodMonitors, monitor)
			continue
		}
		monitor, err := buildServiceMonitor(configured.Name, configured.AcceleratorType, labels, configured.ServiceMonitor)
		if err != nil {
			return Runtime{}, err
		}
		runtime.ServiceMonitors = append(runtime.ServiceMonitors, monitor)
	}
	return runtime, nil
}

func buildPodMonitor(name, acceleratorType string, labels map[string]string, configured *monitorFile) (discovery.PodMonitor, error) {
	monitor, err := normalizeMonitor(name, configured)
	if err != nil {
		return discovery.PodMonitor{}, err
	}
	return discovery.PodMonitor{
		Name: name, AcceleratorType: acceleratorType, Namespaces: monitor.namespaces, AnyNamespace: monitor.anyNamespace, Selector: monitor.selector,
		Port: monitor.port, Path: monitor.path, Scheme: monitor.scheme, Labels: labels,
	}, nil
}

func buildServiceMonitor(name, acceleratorType string, labels map[string]string, configured *monitorFile) (discovery.ServiceMonitor, error) {
	monitor, err := normalizeMonitor(name, configured)
	if err != nil {
		return discovery.ServiceMonitor{}, err
	}
	return discovery.ServiceMonitor{
		Name: name, AcceleratorType: acceleratorType, Namespaces: monitor.namespaces, AnyNamespace: monitor.anyNamespace, Selector: monitor.selector,
		Port: monitor.port, Path: monitor.path, Scheme: monitor.scheme, Labels: labels,
	}, nil
}

type normalizedMonitor struct {
	namespaces   []string
	anyNamespace bool
	selector     metav1.LabelSelector
	port         string
	path         string
	scheme       string
}

func normalizeMonitor(sourceName string, configured *monitorFile) (normalizedMonitor, error) {
	namespaces := uniqueNonEmpty(append(append([]string(nil), configured.Namespaces...), configured.NamespaceSelector.MatchNames...))
	if configured.NamespaceSelector.Any && len(namespaces) > 0 {
		return normalizedMonitor{}, fmt.Errorf("source %q namespaceSelector.any cannot be combined with matchNames or namespaces", sourceName)
	}
	if !configured.NamespaceSelector.Any && len(namespaces) == 0 {
		return normalizedMonitor{}, fmt.Errorf("source %q monitor namespaceSelector must set any or matchNames", sourceName)
	}
	port := strings.TrimSpace(string(configured.Port))
	if port == "" {
		return normalizedMonitor{}, fmt.Errorf("source %q monitor port is required", sourceName)
	}
	if numeric, err := strconv.ParseInt(port, 10, 32); err == nil {
		if numeric < 1 || numeric > 65535 {
			return normalizedMonitor{}, fmt.Errorf("source %q monitor numeric port must be between 1 and 65535", sourceName)
		}
	} else if problems := validation.IsValidPortName(port); len(problems) > 0 {
		return normalizedMonitor{}, fmt.Errorf("source %q monitor port name is invalid: %s", sourceName, strings.Join(problems, ", "))
	}
	path := strings.TrimSpace(configured.Path)
	if path == "" {
		path = "/metrics"
	}
	if !strings.HasPrefix(path, "/") {
		return normalizedMonitor{}, fmt.Errorf("source %q monitor path must start with /", sourceName)
	}
	scheme := strings.ToLower(strings.TrimSpace(configured.Scheme))
	if scheme == "" {
		scheme = "http"
	}
	if scheme != "http" && scheme != "https" {
		return normalizedMonitor{}, fmt.Errorf("source %q monitor scheme must be http or https", sourceName)
	}
	selector := metav1.LabelSelector{MatchLabels: configured.Selector.MatchLabels}
	for _, expression := range configured.Selector.MatchExpressions {
		selector.MatchExpressions = append(selector.MatchExpressions, metav1.LabelSelectorRequirement{
			Key: expression.Key, Operator: metav1.LabelSelectorOperator(expression.Operator), Values: expression.Values,
		})
	}
	if len(selector.MatchLabels) == 0 && len(selector.MatchExpressions) == 0 {
		return normalizedMonitor{}, fmt.Errorf("source %q monitor selector must not be empty", sourceName)
	}
	if _, err := metav1.LabelSelectorAsSelector(&selector); err != nil {
		return normalizedMonitor{}, fmt.Errorf("source %q monitor selector: %w", sourceName, err)
	}
	return normalizedMonitor{namespaces: namespaces, anyNamespace: configured.NamespaceSelector.Any, selector: selector, port: port, path: path, scheme: scheme}, nil
}

func uniqueNonEmpty(values []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func positiveDuration(name, value string) (time.Duration, error) {
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", name, err)
	}
	if duration <= 0 {
		return 0, fmt.Errorf("%s must be positive", name)
	}
	return duration, nil
}

func supportedType(acceleratorType string) bool {
	switch acceleratorType {
	case model.AcceleratorTypeGPU, model.AcceleratorTypeNPU, model.AcceleratorTypeVGPU, model.AcceleratorTypeVNPU:
		return true
	default:
		return false
	}
}
