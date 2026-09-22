package scrapetargets

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"xiaoshiai.cn/accelerator-exporter/pkg/accelerator"
)

type fakePodLister struct {
	pods       map[string][]corev1.Pod
	error      map[string]error
	seen       []metav1.ListOptions
	namespaces []string
}

func (lister *fakePodLister) ListPods(_ context.Context, namespace string, options metav1.ListOptions) (*corev1.PodList, error) {
	lister.seen = append(lister.seen, options)
	lister.namespaces = append(lister.namespaces, namespace)
	if err := lister.error[namespace]; err != nil {
		return nil, err
	}
	return &corev1.PodList{Items: append([]corev1.Pod(nil), lister.pods[namespace]...)}, nil
}

func TestPodProviderDiscoversMatchingRunningPods(t *testing.T) {
	t.Parallel()
	lister := &fakePodLister{pods: map[string][]corev1.Pod{"gpu-operator": {
		{
			ObjectMeta: metav1.ObjectMeta{Name: "dcgm-a", Namespace: "gpu-operator", Labels: map[string]string{"app": "dcgm", "role": "metrics"}},
			Spec:       corev1.PodSpec{NodeName: "node-a", Containers: []corev1.Container{{Ports: []corev1.ContainerPort{{Name: "metrics", ContainerPort: 9400}}}}},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.1"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "dcgm-remote", Namespace: "gpu-operator", Labels: map[string]string{"app": "dcgm", "role": "metrics"}},
			Spec:       corev1.PodSpec{NodeName: "node-b", Containers: []corev1.Container{{Ports: []corev1.ContainerPort{{Name: "metrics", ContainerPort: 9400}}}}},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.3"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "gpu-operator", Labels: map[string]string{"app": "other"}},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Ports: []corev1.ContainerPort{{Name: "metrics", ContainerPort: 9400}}}}},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.2"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "pending", Namespace: "gpu-operator", Labels: map[string]string{"app": "dcgm", "role": "metrics"}},
			Status:     corev1.PodStatus{Phase: corev1.PodPending},
		},
	}}, error: map[string]error{}}
	provider, err := NewPodProvider(lister, "node-a", []Source{{
		Name: "dcgm", Rule: accelerator.RuleNvidiaDCGM, Labels: map[string]string{"model": "A100"},
		PodMonitor: &MonitorSpec{
			Namespaces: []string{"gpu-operator"},
			Selector: LabelSelector{
				MatchLabels:      map[string]string{"app": "dcgm"},
				MatchExpressions: []LabelSelectorRequirement{{Key: "role", Operator: "In", Values: []string{"metrics"}}},
			},
			Port: "metrics", Path: "/metrics", Scheme: "http",
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	targets, err := provider.Targets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 {
		t.Fatalf("targets = %#v", targets)
	}
	target := targets[0]
	// The node is always the one this process runs on; only the pinned model
	// survives from the configuration.
	if target.Name != "dcgm/gpu-operator/dcgm-a" || target.Address != "http://10.0.0.1:9400/metrics" ||
		target.Labels["node"] != "node-a" || target.Labels["model"] != "A100" {
		t.Fatalf("unexpected target: %#v", target)
	}
	if len(lister.seen) != 1 || lister.seen[0].LabelSelector == "" || lister.seen[0].FieldSelector != "spec.nodeName=node-a" {
		t.Fatalf("selector was not passed to Kubernetes: %#v", lister.seen)
	}
}

func TestPodProviderSupportsNumericPortAndPartialErrors(t *testing.T) {
	t.Parallel()
	lister := &fakePodLister{
		pods: map[string][]corev1.Pod{"ok": {{
			ObjectMeta: metav1.ObjectMeta{Name: "vgpu", Namespace: "ok", Labels: map[string]string{"app": "vgpu"}},
			Spec:       corev1.PodSpec{NodeName: "node-vgpu"},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "2001:db8::1"},
		}}},
		error: map[string]error{"failed": errors.New("forbidden")},
	}
	provider, err := NewPodProvider(lister, "node-vgpu", []Source{{
		Name: "vgpu", Rule: accelerator.RuleVolcanoVGPU,
		PodMonitor: &MonitorSpec{
			Namespaces: []string{"ok", "failed"},
			Selector:   LabelSelector{MatchLabels: map[string]string{"app": "vgpu"}},
			Port:       "9394", Path: "/metrics", Scheme: "http",
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	targets, err := provider.Targets(context.Background())
	if err == nil || len(targets) != 1 {
		t.Fatalf("targets = %#v, error = %v", targets, err)
	}
	if targets[0].Address != "http://[2001:db8::1]:9394/metrics" {
		t.Fatalf("address = %q", targets[0].Address)
	}
}

func TestPodProviderSupportsAnyNamespace(t *testing.T) {
	t.Parallel()
	lister := &fakePodLister{pods: map[string][]corev1.Pod{}, error: map[string]error{}}
	provider, err := NewPodProvider(lister, "node-a", []Source{{
		Name: "all-gpus", Rule: accelerator.RuleNvidiaDCGM,
		PodMonitor: &MonitorSpec{
			NamespaceSelector: NamespaceSelector{Any: true},
			Selector:          LabelSelector{MatchLabels: map[string]string{"app": "dcgm"}},
			Port:              "9400", Path: "/metrics", Scheme: "http",
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Targets(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(lister.namespaces) != 1 || lister.namespaces[0] != metav1.NamespaceAll {
		t.Fatalf("listed namespaces = %#v", lister.namespaces)
	}
}

func TestProvidersRequireNodeName(t *testing.T) {
	t.Parallel()
	if _, err := NewPodProvider(&fakePodLister{}, "", nil); err == nil {
		t.Fatal("expected Pod provider to reject an empty node name")
	}
	if _, err := NewServiceProvider(&fakeServiceEndpointLister{}, "", nil); err == nil {
		t.Fatal("expected Service provider to reject an empty node name")
	}
}

func TestCompositeKeepsSuccessfulTargetsOnDiscoveryError(t *testing.T) {
	t.Parallel()
	target := accelerator.Source{Name: "discovered", Address: "http://host/metrics", Rule: accelerator.RuleAscendNPU}
	provider := Composite(
		[]Source{{Name: "discovered"}},
		providerFunc(func(context.Context) ([]accelerator.Source, error) { return []accelerator.Source{target}, nil }),
		providerFunc(func(context.Context) ([]accelerator.Source, error) { return nil, errors.New("discovery failed") }),
	)
	targets, err := provider.Targets(context.Background())
	if err == nil || len(targets) != 1 || targets[0].Name != "discovered" {
		t.Fatalf("targets = %#v, error = %v", targets, err)
	}
}

// TestCompositeOrdersByConfigurationNotByName: the configured order is the
// precedence between sources that report the same observation target, so it has
// to survive the merge. The names here sort the other way round on purpose.
func TestCompositeOrdersByConfigurationNotByName(t *testing.T) {
	t.Parallel()
	provider := Composite(
		[]Source{{Name: "z-authoritative"}, {Name: "a-fallback"}},
		providerFunc(func(context.Context) ([]accelerator.Source, error) {
			return []accelerator.Source{{Name: "a-fallback/ns/pod", Address: "http://a/metrics", Rule: accelerator.RuleVolcanoVGPU}}, nil
		}),
		providerFunc(func(context.Context) ([]accelerator.Source, error) {
			return []accelerator.Source{{Name: "z-authoritative/ns/svc", Address: "http://z/metrics", Rule: accelerator.RuleNvidiaDCGM}}, nil
		}),
	)
	targets, err := provider.Targets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 || targets[0].Name != "z-authoritative/ns/svc" || targets[1].Name != "a-fallback/ns/pod" {
		t.Fatalf("the configured order must decide, not the names: %#v", targets)
	}
}

// TestPodProviderGroupsSourcesInConfigurationOrder is the same guarantee one
// level down: two pod-based sources are reported as two blocks in the order they
// were configured, whatever the matching Pods are called.
func TestPodProviderGroupsSourcesInConfigurationOrder(t *testing.T) {
	t.Parallel()
	ready := func(name, app string) corev1.Pod {
		return corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "gpu", Labels: map[string]string{"app": app}},
			Spec: corev1.PodSpec{
				NodeName:   "node-a",
				Containers: []corev1.Container{{Ports: []corev1.ContainerPort{{Name: "metrics", ContainerPort: 9400}}}},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.1"},
		}
	}
	lister := &fakePodLister{
		pods:  map[string][]corev1.Pod{"gpu": {ready("z-pod", "z"), ready("a-pod", "a")}},
		error: map[string]error{},
	}
	podMonitor := func(app string) *MonitorSpec {
		return &MonitorSpec{
			Namespaces: []string{"gpu"},
			Selector:   LabelSelector{MatchLabels: map[string]string{"app": app}},
			Port:       "metrics",
		}
	}
	provider, err := NewPodProvider(lister, "node-a", []Source{
		{Name: "z-source", Rule: accelerator.RuleNvidiaDCGM, PodMonitor: podMonitor("z")},
		{Name: "a-source", Rule: accelerator.RuleVolcanoVGPU, PodMonitor: podMonitor("a")},
	})
	if err != nil {
		t.Fatal(err)
	}
	targets, err := provider.Targets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 || targets[0].Name != "z-source/gpu/z-pod" || targets[1].Name != "a-source/gpu/a-pod" {
		t.Fatalf("targets = %#v, want the configured source order", targets)
	}
}

type fakeServiceEndpointLister struct {
	services []corev1.Service
	slices   []discoveryv1.EndpointSlice
}

func (lister *fakeServiceEndpointLister) ListServices(context.Context, string, metav1.ListOptions) (*corev1.ServiceList, error) {
	return &corev1.ServiceList{Items: append([]corev1.Service(nil), lister.services...)}, nil
}

func (lister *fakeServiceEndpointLister) ListEndpointSlices(context.Context, string, metav1.ListOptions) (*discoveryv1.EndpointSliceList, error) {
	return &discoveryv1.EndpointSliceList{Items: append([]discoveryv1.EndpointSlice(nil), lister.slices...)}, nil
}

func TestServiceProviderExpandsMatchingServiceEndpointSlices(t *testing.T) {
	t.Parallel()
	portName := "metrics"
	port := int32(8082)
	node := "npu-node-a"
	remoteNode := "npu-node-b"
	ready := true
	notReady := false
	lister := &fakeServiceEndpointLister{
		services: []corev1.Service{
			{ObjectMeta: metav1.ObjectMeta{Name: "npu-exporter", Namespace: "npu", Labels: map[string]string{"app": "npu-exporter"}}, Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Name: portName, Port: port}}}},
			{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "npu", Labels: map[string]string{"app": "other"}}},
		},
		slices: []discoveryv1.EndpointSlice{{
			ObjectMeta: metav1.ObjectMeta{Name: "npu-exporter-abc", Namespace: "npu", Labels: map[string]string{discoveryv1.LabelServiceName: "npu-exporter"}},
			Ports:      []discoveryv1.EndpointPort{{Name: &portName, Port: &port}},
			Endpoints: []discoveryv1.Endpoint{
				{Addresses: []string{"10.1.0.1"}, NodeName: &node, Conditions: discoveryv1.EndpointConditions{Ready: &ready}},
				{Addresses: []string{"10.1.0.3"}, NodeName: &remoteNode, Conditions: discoveryv1.EndpointConditions{Ready: &ready}},
				{Addresses: []string{"10.1.0.4"}, Conditions: discoveryv1.EndpointConditions{Ready: &ready}},
				{Addresses: []string{"10.1.0.2"}, Conditions: discoveryv1.EndpointConditions{Ready: &notReady}},
			},
		}},
	}
	provider, err := NewServiceProvider(lister, node, []Source{{
		Name: "npu-service", Rule: accelerator.RuleAscendNPU,
		ServiceMonitor: &MonitorSpec{
			Namespaces: []string{"npu"},
			Selector:   LabelSelector{MatchLabels: map[string]string{"app": "npu-exporter"}},
			Port:       Port(portName), Path: "/metrics", Scheme: "http",
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	targets, err := provider.Targets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 {
		t.Fatalf("targets = %#v", targets)
	}
	if targets[0].Address != "http://10.1.0.1:8082/metrics" || targets[0].Labels["node"] != node || targets[0].Rule != accelerator.RuleAscendNPU {
		t.Fatalf("unexpected target: %#v", targets[0])
	}
}
