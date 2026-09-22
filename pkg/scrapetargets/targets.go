// Package scrapetargets resolves the configured accelerator metrics sources to
// the concrete endpoints this node has to scrape.
//
// It reads the Node's Pods, Services and EndpointSlices and nothing else: it
// reports where to scrape, never what was scraped.
package scrapetargets

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	typeddiscoveryv1 "k8s.io/client-go/kubernetes/typed/discovery/v1"

	"xiaoshiai.cn/accelerator-exporter/pkg/accelerator"
)

// Provider resolves the metrics endpoints of one node.
type Provider interface {
	Targets(context.Context) ([]accelerator.Source, error)
}

// Composite combines multiple providers into the target list of one node.
// Targets returned alongside an error remain usable when one Kubernetes
// discovery provider temporarily fails.
//
// The merged list follows the order the sources are configured in, not the
// alphabetical order of their names. That order is the precedence between
// sources that report the same metric for the same observation target: the
// consumer keeps the first series it sees and counts the rest as duplicates, so
// the source configured first is the one whose reading survives. A target whose
// source is not in the list is placed last; two such targets fall back to their
// names, which keeps the order stable between rounds.
func Composite(sources []Source, providers ...Provider) Provider {
	order := make(map[string]int, len(sources))
	for index, source := range sources {
		if _, exists := order[source.Name]; !exists {
			order[source.Name] = index
		}
	}
	return providerFunc(func(ctx context.Context) ([]accelerator.Source, error) {
		var targets []accelerator.Source
		var providerErrors []error
		for _, provider := range providers {
			if provider == nil {
				continue
			}
			discovered, err := provider.Targets(ctx)
			targets = append(targets, discovered...)
			if err != nil {
				providerErrors = append(providerErrors, err)
			}
		}
		sort.SliceStable(targets, func(i, j int) bool {
			left, right := sourceOrder(order, targets[i].Name), sourceOrder(order, targets[j].Name)
			if left != right {
				return left < right
			}
			return targets[i].Name < targets[j].Name
		})
		return targets, errors.Join(providerErrors...)
	})
}

// sourceOrder is where the source of one target sits in the configuration. A
// target is named <source name>/<where it was found>, so the source name is its
// first segment.
func sourceOrder(order map[string]int, targetName string) int {
	name, _, _ := strings.Cut(targetName, "/")
	if position, ok := order[name]; ok {
		return position
	}
	return len(order)
}

type providerFunc func(context.Context) ([]accelerator.Source, error)

func (function providerFunc) Targets(ctx context.Context) ([]accelerator.Source, error) {
	return function(ctx)
}

// PodLister is the small Kubernetes seam required by PodProvider.
type PodLister interface {
	ListPods(context.Context, string, metav1.ListOptions) (*corev1.PodList, error)
}

type corePodLister struct {
	client typedcorev1.CoreV1Interface
}

// NewCorePodLister adapts a Kubernetes CoreV1 client to PodLister.
func NewCorePodLister(client typedcorev1.CoreV1Interface) PodLister {
	return &corePodLister{client: client}
}

func (lister *corePodLister) ListPods(ctx context.Context, namespace string, options metav1.ListOptions) (*corev1.PodList, error) {
	return lister.client.Pods(namespace).List(ctx, options)
}

// ServiceEndpointLister is the Kubernetes seam required by ServiceProvider.
type ServiceEndpointLister interface {
	ListServices(context.Context, string, metav1.ListOptions) (*corev1.ServiceList, error)
	ListEndpointSlices(context.Context, string, metav1.ListOptions) (*discoveryv1.EndpointSliceList, error)
}

type serviceEndpointLister struct {
	core      typedcorev1.CoreV1Interface
	discovery typeddiscoveryv1.DiscoveryV1Interface
}

// NewServiceEndpointLister adapts Kubernetes clients to ServiceEndpointLister.
func NewServiceEndpointLister(core typedcorev1.CoreV1Interface, discovery typeddiscoveryv1.DiscoveryV1Interface) ServiceEndpointLister {
	return &serviceEndpointLister{core: core, discovery: discovery}
}

func (lister *serviceEndpointLister) ListServices(ctx context.Context, namespace string, options metav1.ListOptions) (*corev1.ServiceList, error) {
	return lister.core.Services(namespace).List(ctx, options)
}

func (lister *serviceEndpointLister) ListEndpointSlices(ctx context.Context, namespace string, options metav1.ListOptions) (*discoveryv1.EndpointSliceList, error) {
	return lister.discovery.EndpointSlices(namespace).List(ctx, options)
}

// PodProvider resolves every Pod-based source to the Pod-IP metrics endpoints of
// one node. Sources that use a ServiceMonitor are ignored, so the same source
// list can be handed to every provider.
type PodProvider struct {
	lister   PodLister
	nodeName string
	monitors []resolved
}

// NewPodProvider creates the Pod-based target provider.
func NewPodProvider(lister PodLister, nodeName string, sources []Source) (*PodProvider, error) {
	if lister == nil {
		return nil, fmt.Errorf("Pod lister is required")
	}
	nodeName = strings.TrimSpace(nodeName)
	if nodeName == "" {
		return nil, fmt.Errorf("node name is required")
	}
	monitors, err := normalizeSources(sources, true)
	if err != nil {
		return nil, err
	}
	return &PodProvider{lister: lister, nodeName: nodeName, monitors: monitors}, nil
}

// Targets resolves matching running Pods to direct Pod-IP metrics URLs.
//
// The result groups each source's targets together and keeps the sources in the
// order they are configured, because that order is the precedence between
// sources. The order within one source is whatever the API returned; nothing
// depends on it, and Composite imposes the final order anyway.
func (provider *PodProvider) Targets(ctx context.Context) ([]accelerator.Source, error) {
	var targets []accelerator.Source
	var discoveryErrors []error
	for _, monitor := range provider.monitors {
		selector, err := metav1.LabelSelectorAsSelector(&monitor.selector)
		if err != nil {
			discoveryErrors = append(discoveryErrors, fmt.Errorf("source %q selector: %w", monitor.name, err))
			continue
		}
		for _, namespace := range monitor.queryNamespaces() {
			pods, err := provider.lister.ListPods(ctx, namespace, metav1.ListOptions{
				LabelSelector: selector.String(),
				FieldSelector: fields.OneTermEqualSelector("spec.nodeName", provider.nodeName).String(),
			})
			if err != nil {
				discoveryErrors = append(discoveryErrors, fmt.Errorf("source %q list namespace %q: %w", monitor.name, namespace, err))
				continue
			}
			for index := range pods.Items {
				pod := &pods.Items[index]
				if pod.Spec.NodeName != provider.nodeName || !podMatches(pod, selector) || pod.Status.Phase != corev1.PodRunning || pod.Status.PodIP == "" {
					continue
				}
				port, ok := resolvePort(pod, monitor.port)
				if !ok {
					discoveryErrors = append(discoveryErrors, fmt.Errorf("source %q Pod %s/%s has no container port %q", monitor.name, pod.Namespace, pod.Name, monitor.port))
					continue
				}
				endpoint := url.URL{
					Scheme: monitor.scheme,
					Host:   net.JoinHostPort(pod.Status.PodIP, strconv.Itoa(int(port))),
					Path:   monitor.path,
				}
				targets = append(targets, accelerator.Source{
					Name:    monitor.name + "/" + pod.Namespace + "/" + pod.Name,
					Address: endpoint.String(),
					Rule:    monitor.rule,
					Labels:  sourceLabels(monitor, provider.nodeName),
				})
			}
		}
	}
	return targets, errors.Join(discoveryErrors...)
}

// ServiceProvider resolves Services by label and expands each Service through
// EndpointSlices so every backend metrics endpoint is scraped directly.
type ServiceProvider struct {
	lister   ServiceEndpointLister
	nodeName string
	monitors []resolved
}

// NewServiceProvider creates the Service-based target provider.
func NewServiceProvider(lister ServiceEndpointLister, nodeName string, sources []Source) (*ServiceProvider, error) {
	if lister == nil {
		return nil, fmt.Errorf("Service and EndpointSlice lister is required")
	}
	nodeName = strings.TrimSpace(nodeName)
	if nodeName == "" {
		return nil, fmt.Errorf("node name is required")
	}
	monitors, err := normalizeSources(sources, false)
	if err != nil {
		return nil, err
	}
	return &ServiceProvider{lister: lister, nodeName: nodeName, monitors: monitors}, nil
}

// Targets resolves selected Services to ready EndpointSlice addresses.
//
// Like the Pod provider, it groups each source's targets together and keeps the
// sources in the order they are configured.
func (provider *ServiceProvider) Targets(ctx context.Context) ([]accelerator.Source, error) {
	seen := map[string]struct{}{}
	var targets []accelerator.Source
	var discoveryErrors []error
	for _, monitor := range provider.monitors {
		selector, err := metav1.LabelSelectorAsSelector(&monitor.selector)
		if err != nil {
			discoveryErrors = append(discoveryErrors, fmt.Errorf("source %q selector: %w", monitor.name, err))
			continue
		}
		for _, namespace := range monitor.queryNamespaces() {
			services, err := provider.lister.ListServices(ctx, namespace, metav1.ListOptions{LabelSelector: selector.String()})
			if err != nil {
				discoveryErrors = append(discoveryErrors, fmt.Errorf("source %q list namespace %q: %w", monitor.name, namespace, err))
				continue
			}
			for serviceIndex := range services.Items {
				service := &services.Items[serviceIndex]
				if !selector.Matches(labels.Set(service.Labels)) {
					continue
				}
				portName, ok := resolveServicePort(service, monitor.port)
				if !ok {
					discoveryErrors = append(discoveryErrors, fmt.Errorf("source %q Service %s/%s has no port %q", monitor.name, service.Namespace, service.Name, monitor.port))
					continue
				}
				sliceSelector := labels.Set{discoveryv1.LabelServiceName: service.Name}.AsSelector().String()
				slices, err := provider.lister.ListEndpointSlices(ctx, service.Namespace, metav1.ListOptions{LabelSelector: sliceSelector})
				if err != nil {
					discoveryErrors = append(discoveryErrors, fmt.Errorf("source %q list EndpointSlices for %s/%s: %w", monitor.name, service.Namespace, service.Name, err))
					continue
				}
				for sliceIndex := range slices.Items {
					slice := &slices.Items[sliceIndex]
					if slice.Labels[discoveryv1.LabelServiceName] != service.Name {
						continue
					}
					endpointPort, ok := resolveEndpointSlicePort(slice, portName)
					if !ok {
						discoveryErrors = append(discoveryErrors, fmt.Errorf("source %q EndpointSlice %s/%s has no port for Service port %q", monitor.name, slice.Namespace, slice.Name, monitor.port))
						continue
					}
					for _, endpoint := range slice.Endpoints {
						if endpoint.Conditions.Ready != nil && !*endpoint.Conditions.Ready {
							continue
						}
						// An endpoint without a nodeName belongs to no node, and
						// guessing would scrape another node's device metrics.
						if endpoint.NodeName == nil || *endpoint.NodeName != provider.nodeName {
							continue
						}
						for _, address := range endpoint.Addresses {
							host := net.JoinHostPort(address, strconv.Itoa(int(endpointPort)))
							metricsURL := url.URL{Scheme: monitor.scheme, Host: host, Path: monitor.path}
							name := monitor.name + "/" + service.Namespace + "/" + service.Name + "/" + host
							// One endpoint can be reached through more than one
							// Service, so an address is reported once.
							if _, exists := seen[name]; exists {
								continue
							}
							seen[name] = struct{}{}
							targets = append(targets, accelerator.Source{
								Name: name, Address: metricsURL.String(), Rule: monitor.rule,
								Labels: sourceLabels(monitor, provider.nodeName),
							})
						}
					}
				}
			}
		}
	}
	return targets, errors.Join(discoveryErrors...)
}

// sourceLabels is the label set every target of one source carries. The node is
// the one this process runs on; a source can only pin the model.
func sourceLabels(monitor resolved, nodeName string) map[string]string {
	labels := make(map[string]string, len(monitor.labels)+1)
	for name, value := range monitor.labels {
		if value != "" {
			labels[name] = value
		}
	}
	labels[accelerator.LabelNode] = nodeName
	return labels
}

func podMatches(pod *corev1.Pod, selector labels.Selector) bool {
	return selector.Matches(labels.Set(pod.Labels))
}

func resolvePort(pod *corev1.Pod, configured string) (int32, bool) {
	if numeric, err := strconv.ParseInt(configured, 10, 32); err == nil && numeric > 0 && numeric <= 65535 {
		return int32(numeric), true
	}
	for _, container := range pod.Spec.Containers {
		for _, port := range container.Ports {
			if port.Name == configured {
				return port.ContainerPort, true
			}
		}
	}
	return 0, false
}

func resolveServicePort(service *corev1.Service, configured string) (string, bool) {
	numeric, numericError := strconv.ParseInt(configured, 10, 32)
	for _, port := range service.Spec.Ports {
		if port.Name == configured || (numericError == nil && int64(port.Port) == numeric) {
			return port.Name, true
		}
	}
	return "", false
}

func resolveEndpointSlicePort(slice *discoveryv1.EndpointSlice, portName string) (int32, bool) {
	for _, port := range slice.Ports {
		if port.Port == nil {
			continue
		}
		if portName == "" || (port.Name != nil && *port.Name == portName) {
			return *port.Port, true
		}
	}
	return 0, false
}

func uniqueStrings(values []string) []string {
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
