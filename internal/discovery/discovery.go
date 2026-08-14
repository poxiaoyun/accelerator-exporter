// Package discovery resolves configured source definitions to concrete metrics
// addresses. It supports node-local PodMonitor- and ServiceMonitor-style
// Kubernetes selection.
package discovery

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

	"xiaoshiai.cn/rune/internal/model"
)

// Provider discovers concrete metrics targets.
type Provider interface {
	Targets(context.Context) ([]model.Source, error)
}

// Composite combines multiple providers. Targets returned alongside an error
// remain usable when one Kubernetes discovery provider temporarily fails.
func Composite(providers ...Provider) Provider {
	return providerFunc(func(ctx context.Context) ([]model.Source, error) {
		var targets []model.Source
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
		sort.Slice(targets, func(i, j int) bool { return targets[i].Name < targets[j].Name })
		return targets, errors.Join(providerErrors...)
	})
}

type providerFunc func(context.Context) ([]model.Source, error)

func (function providerFunc) Targets(ctx context.Context) ([]model.Source, error) {
	return function(ctx)
}

// PodMonitor describes one PodMonitor-style metrics source.
type PodMonitor struct {
	Name            string
	AcceleratorType string
	Namespaces      []string
	AnyNamespace    bool
	Selector        metav1.LabelSelector
	Port            string
	Path            string
	Scheme          string
	Labels          map[string]string
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

// ServiceMonitor describes one ServiceMonitor-style metrics source.
type ServiceMonitor struct {
	Name            string
	AcceleratorType string
	Namespaces      []string
	AnyNamespace    bool
	Selector        metav1.LabelSelector
	Port            string
	Path            string
	Scheme          string
	Labels          map[string]string
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

// PodProvider discovers every running Pod matching each configured selector.
type PodProvider struct {
	lister   PodLister
	nodeName string
	monitors []PodMonitor
}

// NewPodProvider creates a PodMonitor-style target provider.
func NewPodProvider(lister PodLister, nodeName string, monitors []PodMonitor) (*PodProvider, error) {
	if lister == nil {
		return nil, fmt.Errorf("Pod lister is required")
	}
	nodeName = strings.TrimSpace(nodeName)
	if nodeName == "" {
		return nil, fmt.Errorf("node name is required")
	}
	for _, monitor := range monitors {
		if monitor.Name == "" || monitor.Port == "" || (len(monitor.Namespaces) == 0 && !monitor.AnyNamespace) {
			return nil, fmt.Errorf("Pod monitor requires name, namespaces, and port")
		}
		if _, err := metav1.LabelSelectorAsSelector(&monitor.Selector); err != nil {
			return nil, fmt.Errorf("Pod monitor %q selector: %w", monitor.Name, err)
		}
	}
	return &PodProvider{lister: lister, nodeName: nodeName, monitors: append([]PodMonitor(nil), monitors...)}, nil
}

// Targets resolves matching running Pods to direct Pod-IP metrics URLs.
func (provider *PodProvider) Targets(ctx context.Context) ([]model.Source, error) {
	var targets []model.Source
	var discoveryErrors []error
	for _, monitor := range provider.monitors {
		selector, err := metav1.LabelSelectorAsSelector(&monitor.Selector)
		if err != nil {
			discoveryErrors = append(discoveryErrors, fmt.Errorf("Pod monitor %q selector: %w", monitor.Name, err))
			continue
		}
		for _, namespace := range monitorNamespaces(monitor.Namespaces, monitor.AnyNamespace) {
			pods, err := provider.lister.ListPods(ctx, namespace, metav1.ListOptions{
				LabelSelector: selector.String(),
				FieldSelector: fields.OneTermEqualSelector("spec.nodeName", provider.nodeName).String(),
			})
			if err != nil {
				discoveryErrors = append(discoveryErrors, fmt.Errorf("Pod monitor %q list namespace %q: %w", monitor.Name, namespace, err))
				continue
			}
			for index := range pods.Items {
				pod := &pods.Items[index]
				if pod.Spec.NodeName != provider.nodeName || !podMatches(pod, selector) || pod.Status.Phase != corev1.PodRunning || pod.Status.PodIP == "" {
					continue
				}
				port, ok := resolvePort(pod, monitor.Port)
				if !ok {
					discoveryErrors = append(discoveryErrors, fmt.Errorf("Pod monitor %q Pod %s/%s has no container port %q", monitor.Name, pod.Namespace, pod.Name, monitor.Port))
					continue
				}
				labels := cloneLabels(monitor.Labels)
				labels["node"] = provider.nodeName
				endpoint := url.URL{
					Scheme: monitor.Scheme,
					Host:   net.JoinHostPort(pod.Status.PodIP, strconv.Itoa(int(port))),
					Path:   monitor.Path,
				}
				targets = append(targets, model.Source{
					Name:            monitor.Name + "/" + pod.Namespace + "/" + pod.Name,
					Address:         endpoint.String(),
					AcceleratorType: monitor.AcceleratorType,
					Labels:          labels,
				})
			}
		}
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].Name < targets[j].Name })
	return targets, errors.Join(discoveryErrors...)
}

// ServiceProvider discovers Services by label and expands each Service through
// EndpointSlices so every backend metrics endpoint is scraped directly.
type ServiceProvider struct {
	lister   ServiceEndpointLister
	nodeName string
	monitors []ServiceMonitor
}

// NewServiceProvider creates a ServiceMonitor-style target provider.
func NewServiceProvider(lister ServiceEndpointLister, nodeName string, monitors []ServiceMonitor) (*ServiceProvider, error) {
	if lister == nil {
		return nil, fmt.Errorf("Service and EndpointSlice lister is required")
	}
	nodeName = strings.TrimSpace(nodeName)
	if nodeName == "" {
		return nil, fmt.Errorf("node name is required")
	}
	for _, monitor := range monitors {
		if monitor.Name == "" || monitor.Port == "" || (len(monitor.Namespaces) == 0 && !monitor.AnyNamespace) {
			return nil, fmt.Errorf("Service monitor requires name, namespaces, and port")
		}
		if _, err := metav1.LabelSelectorAsSelector(&monitor.Selector); err != nil {
			return nil, fmt.Errorf("Service monitor %q selector: %w", monitor.Name, err)
		}
	}
	return &ServiceProvider{lister: lister, nodeName: nodeName, monitors: append([]ServiceMonitor(nil), monitors...)}, nil
}

// Targets resolves selected Services to ready EndpointSlice addresses.
func (provider *ServiceProvider) Targets(ctx context.Context) ([]model.Source, error) {
	targetsByName := map[string]model.Source{}
	var discoveryErrors []error
	for _, monitor := range provider.monitors {
		selector, err := metav1.LabelSelectorAsSelector(&monitor.Selector)
		if err != nil {
			discoveryErrors = append(discoveryErrors, fmt.Errorf("Service monitor %q selector: %w", monitor.Name, err))
			continue
		}
		for _, namespace := range monitorNamespaces(monitor.Namespaces, monitor.AnyNamespace) {
			services, err := provider.lister.ListServices(ctx, namespace, metav1.ListOptions{LabelSelector: selector.String()})
			if err != nil {
				discoveryErrors = append(discoveryErrors, fmt.Errorf("Service monitor %q list namespace %q: %w", monitor.Name, namespace, err))
				continue
			}
			for serviceIndex := range services.Items {
				service := &services.Items[serviceIndex]
				if !selector.Matches(labels.Set(service.Labels)) {
					continue
				}
				portName, ok := resolveServicePort(service, monitor.Port)
				if !ok {
					discoveryErrors = append(discoveryErrors, fmt.Errorf("Service monitor %q Service %s/%s has no port %q", monitor.Name, service.Namespace, service.Name, monitor.Port))
					continue
				}
				sliceSelector := labels.Set{discoveryv1.LabelServiceName: service.Name}.AsSelector().String()
				slices, err := provider.lister.ListEndpointSlices(ctx, service.Namespace, metav1.ListOptions{LabelSelector: sliceSelector})
				if err != nil {
					discoveryErrors = append(discoveryErrors, fmt.Errorf("Service monitor %q list EndpointSlices for %s/%s: %w", monitor.Name, service.Namespace, service.Name, err))
					continue
				}
				for sliceIndex := range slices.Items {
					slice := &slices.Items[sliceIndex]
					if slice.Labels[discoveryv1.LabelServiceName] != service.Name {
						continue
					}
					endpointPort, ok := resolveEndpointSlicePort(slice, portName)
					if !ok {
						discoveryErrors = append(discoveryErrors, fmt.Errorf("Service monitor %q EndpointSlice %s/%s has no port for Service port %q", monitor.Name, slice.Namespace, slice.Name, monitor.Port))
						continue
					}
					for _, endpoint := range slice.Endpoints {
						if endpoint.Conditions.Ready != nil && !*endpoint.Conditions.Ready {
							continue
						}
						if endpoint.NodeName == nil || *endpoint.NodeName != provider.nodeName {
							continue
						}
						for _, address := range endpoint.Addresses {
							labels := cloneLabels(monitor.Labels)
							labels["node"] = provider.nodeName
							metricsURL := url.URL{Scheme: monitor.Scheme, Host: net.JoinHostPort(address, strconv.Itoa(int(endpointPort))), Path: monitor.Path}
							name := monitor.Name + "/" + service.Namespace + "/" + service.Name + "/" + net.JoinHostPort(address, strconv.Itoa(int(endpointPort)))
							targetsByName[name] = model.Source{
								Name: name, Address: metricsURL.String(), AcceleratorType: monitor.AcceleratorType, Labels: labels,
							}
						}
					}
				}
			}
		}
	}
	targets := make([]model.Source, 0, len(targetsByName))
	for _, target := range targetsByName {
		targets = append(targets, target)
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].Name < targets[j].Name })
	return targets, errors.Join(discoveryErrors...)
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

func monitorNamespaces(namespaces []string, anyNamespace bool) []string {
	if anyNamespace {
		return []string{metav1.NamespaceAll}
	}
	return uniqueStrings(namespaces)
}

func cloneLabels(source map[string]string) map[string]string {
	cloned := make(map[string]string, len(source)+1)
	for name, value := range source {
		cloned[name] = value
	}
	return cloned
}
