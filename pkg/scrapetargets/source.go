package scrapetargets

import (
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	"xiaoshiai.cn/accelerator-exporter/pkg/accelerator"
)

// Source is one configured accelerator metrics source: a name, the rule that
// interprets its metrics, and exactly one way of finding its endpoints.
//
// The shape is what the configuration file carries. Validate checks it, and the
// providers turn it into the endpoints of one node.
type Source struct {
	// Name identifies the source. It is unique within a configuration and
	// prefixes the name of every target the source resolves.
	Name string `yaml:"name" description:"Source name, unique within the configuration"`
	// Rule is the built-in rule that interprets this source's metrics. The rule
	// decides device_type, device_mode, vendor and the identity of every
	// observation target, so a source never states them itself.
	Rule string `yaml:"rule" description:"Built-in rule that interprets this source"`
	// Labels pin the fixed attributes a vendor metric omits.
	Labels map[string]string `yaml:"labels,omitempty" description:"Optional node and model overrides"`
	// PodMonitor finds endpoints by listing matching Pods on this node.
	PodMonitor *MonitorSpec `yaml:"podMonitor,omitempty" description:"Discover endpoints from Pods"`
	// ServiceMonitor finds endpoints through a Service's EndpointSlices.
	ServiceMonitor *MonitorSpec `yaml:"serviceMonitor,omitempty" description:"Discover endpoints from Service endpoints"`
}

// MonitorSpec is how one source's endpoints are found.
type MonitorSpec struct {
	// Namespaces is a shorthand for NamespaceSelector.MatchNames.
	Namespaces []string `yaml:"namespaces,omitempty" description:"Namespaces to search"`
	// NamespaceSelector selects the namespaces to search.
	NamespaceSelector NamespaceSelector `yaml:"namespaceSelector,omitempty" description:"Namespace selector"`
	// Selector selects the Pods or Services to scrape.
	Selector LabelSelector `yaml:"selector" description:"Label selector"`
	// Port is the container or Service port name, or its number.
	Port Port `yaml:"port" description:"Named or numeric port"`
	// Path is the metrics path. Empty means /metrics.
	Path string `yaml:"path,omitempty" description:"Metrics path, defaults to /metrics"`
	// Scheme is http or https. Empty means http.
	Scheme string `yaml:"scheme,omitempty" description:"http or https, defaults to http"`
}

// NamespaceSelector selects the namespaces a source searches.
type NamespaceSelector struct {
	// Any searches every namespace.
	Any bool `yaml:"any,omitempty" description:"Search every namespace"`
	// MatchNames limits the search to these namespaces.
	MatchNames []string `yaml:"matchNames,omitempty" description:"Namespaces to search"`
}

// LabelSelector is metav1.LabelSelector with the field names a configuration
// file uses. It is spelled out rather than reused because the Kubernetes type
// carries JSON tags, and the YAML decoder reads different ones.
type LabelSelector struct {
	MatchLabels      map[string]string          `yaml:"matchLabels,omitempty" description:"Exact label matches"`
	MatchExpressions []LabelSelectorRequirement `yaml:"matchExpressions,omitempty" description:"Label expressions"`
}

// LabelSelectorRequirement is one matchExpressions entry.
type LabelSelectorRequirement struct {
	Key      string   `yaml:"key" description:"Label key"`
	Operator string   `yaml:"operator" description:"In, NotIn, Exists or DoesNotExist"`
	Values   []string `yaml:"values,omitempty" description:"Values the operator compares against"`
}

// empty reports whether the selector would match everything, which is never what
// a source means: an unfiltered list would scrape every Pod on the node.
func (selector LabelSelector) empty() bool {
	return len(selector.MatchLabels) == 0 && len(selector.MatchExpressions) == 0
}

// toKubernetes converts to the type the Kubernetes clients take.
func (selector LabelSelector) toKubernetes() metav1.LabelSelector {
	converted := metav1.LabelSelector{MatchLabels: selector.MatchLabels}
	for _, expression := range selector.MatchExpressions {
		converted.MatchExpressions = append(converted.MatchExpressions, metav1.LabelSelectorRequirement{
			Key: expression.Key, Operator: metav1.LabelSelectorOperator(expression.Operator), Values: expression.Values,
		})
	}
	return converted
}

// Port is a port name or a port number. A configuration file may write either.
type Port string

// UnmarshalYAML accepts a name or an integer, so an unquoted port number is a
// port and not a type error.
func (port *Port) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode || (node.Tag != "!!str" && node.Tag != "!!int") {
		return fmt.Errorf("port must be a name or an integer")
	}
	*port = Port(node.Value)
	return nil
}

// sourceLabelAllowlist is the only set of labels a source may pin.
//
// The contract's identity and classification labels are resolved from the
// samples by the rule, and a configured constant would override what was
// observed. `node` is not on the list either: it is always the node this process
// runs on, which discovery supplies. Every other label would never reach the
// public contract anyway, because a device-layer metric carries only the label
// groups the specification defines.
var sourceLabelAllowlist = map[string]bool{
	accelerator.LabelModel: true,
}

// Validate reports whether the source can be collected.
func (source Source) Validate() error {
	name := strings.TrimSpace(source.Name)
	if name == "" {
		return fmt.Errorf("source name is required")
	}
	if _, ok := accelerator.RuleByName(strings.TrimSpace(source.Rule)); !ok {
		return fmt.Errorf(
			"source %q has unknown rule %q, expected one of %s",
			name, source.Rule, strings.Join(accelerator.RuleNames(), ", "),
		)
	}
	for label := range source.Labels {
		if !validLabelName(label) {
			return fmt.Errorf("source %q has invalid label name %q", name, label)
		}
		if !sourceLabelAllowlist[label] {
			return fmt.Errorf(
				"source %q must not set label %q: only model may be pinned, node is always the node this process runs on, and every other label is resolved from the samples",
				name, label,
			)
		}
	}
	switch {
	case source.PodMonitor != nil && source.ServiceMonitor != nil:
		return fmt.Errorf("source %q must configure exactly one of podMonitor or serviceMonitor", name)
	case source.PodMonitor == nil && source.ServiceMonitor == nil:
		return fmt.Errorf("source %q must configure exactly one of podMonitor or serviceMonitor", name)
	case source.PodMonitor != nil:
		return source.PodMonitor.validate(name)
	default:
		return source.ServiceMonitor.validate(name)
	}
}

func (spec MonitorSpec) validate(sourceName string) error {
	namespaces := spec.namespaces()
	if spec.NamespaceSelector.Any && len(namespaces) > 0 {
		return fmt.Errorf("source %q namespaceSelector.any cannot be combined with matchNames or namespaces", sourceName)
	}
	if !spec.NamespaceSelector.Any && len(namespaces) == 0 {
		return fmt.Errorf("source %q must set namespaceSelector.any or select at least one namespace", sourceName)
	}
	port := strings.TrimSpace(string(spec.Port))
	if port == "" {
		return fmt.Errorf("source %q port is required", sourceName)
	}
	if numeric, err := strconv.ParseInt(port, 10, 32); err == nil {
		if numeric < 1 || numeric > 65535 {
			return fmt.Errorf("source %q numeric port must be between 1 and 65535", sourceName)
		}
	} else if problems := validation.IsValidPortName(port); len(problems) > 0 {
		return fmt.Errorf("source %q port name is invalid: %s", sourceName, strings.Join(problems, ", "))
	}
	if path := strings.TrimSpace(spec.Path); path != "" && !strings.HasPrefix(path, "/") {
		return fmt.Errorf("source %q path must start with /", sourceName)
	}
	switch strings.ToLower(strings.TrimSpace(spec.Scheme)) {
	case "", "http", "https":
	default:
		return fmt.Errorf("source %q scheme must be http or https", sourceName)
	}
	if spec.Selector.empty() {
		return fmt.Errorf("source %q selector must not be empty", sourceName)
	}
	selector := spec.Selector.toKubernetes()
	if _, err := metav1.LabelSelectorAsSelector(&selector); err != nil {
		return fmt.Errorf("source %q selector: %w", sourceName, err)
	}
	return nil
}

// namespaces is the union of the two ways of naming namespaces.
func (spec MonitorSpec) namespaces() []string {
	combined := append([]string(nil), spec.Namespaces...)
	combined = append(combined, spec.NamespaceSelector.MatchNames...)
	return uniqueStrings(combined)
}

// resolved is the normalized form of one monitor: everything a provider needs,
// with every default and shorthand already applied.
type resolved struct {
	name         string
	rule         string
	labels       map[string]string
	namespaces   []string
	anyNamespace bool
	selector     metav1.LabelSelector
	port         string
	path         string
	scheme       string
}

// resolve applies the monitor's defaults and carries the source identity into the
// provider, so the provider never looks at a raw configuration value.
func (source Source) resolve(spec MonitorSpec) resolved {
	path := strings.TrimSpace(spec.Path)
	if path == "" {
		path = "/metrics"
	}
	scheme := strings.ToLower(strings.TrimSpace(spec.Scheme))
	if scheme == "" {
		scheme = "http"
	}
	return resolved{
		name:         strings.TrimSpace(source.Name),
		rule:         strings.TrimSpace(source.Rule),
		labels:       source.Labels,
		namespaces:   spec.namespaces(),
		anyNamespace: spec.NamespaceSelector.Any,
		selector:     spec.Selector.toKubernetes(),
		port:         strings.TrimSpace(string(spec.Port)),
		path:         path,
		scheme:       scheme,
	}
}

// queryNamespaces is the namespace list to list, with the all-namespaces
// shorthand resolved.
func (monitor resolved) queryNamespaces() []string {
	if monitor.anyNamespace {
		return []string{metav1.NamespaceAll}
	}
	return monitor.namespaces
}

// normalizeSources validates every source and splits the ones a provider handles.
func normalizeSources(sources []Source, wantPod bool) ([]resolved, error) {
	var monitors []resolved
	for _, source := range sources {
		if err := source.Validate(); err != nil {
			return nil, err
		}
		switch {
		case wantPod && source.PodMonitor != nil:
			monitors = append(monitors, source.resolve(*source.PodMonitor))
		case !wantPod && source.ServiceMonitor != nil:
			monitors = append(monitors, source.resolve(*source.ServiceMonitor))
		}
	}
	return monitors, nil
}

func validLabelName(name string) bool {
	if name == "" {
		return false
	}
	for index, character := range name {
		switch {
		case character >= 'a' && character <= 'z',
			character >= 'A' && character <= 'Z',
			character == '_':
		case character >= '0' && character <= '9' && index > 0:
		default:
			return false
		}
	}
	return true
}
