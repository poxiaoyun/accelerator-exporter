package discovery

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"xiaoshiai.cn/rune/internal/model"
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
			ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "gpu-operator", Labels: map[string]string{"app": "other"}},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Ports: []corev1.ContainerPort{{Name: "metrics", ContainerPort: 9400}}}}},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.2"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "pending", Namespace: "gpu-operator", Labels: map[string]string{"app": "dcgm", "role": "metrics"}},
			Status:     corev1.PodStatus{Phase: corev1.PodPending},
		},
	}}, error: map[string]error{}}
	provider, err := NewPodProvider(lister, []PodMonitor{{
		Name: "dcgm", AcceleratorType: model.AcceleratorTypeGPU, Namespaces: []string{"gpu-operator"},
		Selector: metav1.LabelSelector{
			MatchLabels:      map[string]string{"app": "dcgm"},
			MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "role", Operator: metav1.LabelSelectorOpIn, Values: []string{"metrics"}}},
		},
		Port: "metrics", Path: "/metrics", Scheme: "http",
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
	if target.Name != "dcgm/gpu-operator/dcgm-a" || target.Address != "http://10.0.0.1:9400/metrics" || target.Labels["node"] != "node-a" {
		t.Fatalf("unexpected target: %#v", target)
	}
	if len(lister.seen) != 1 || lister.seen[0].LabelSelector == "" {
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
	provider, err := NewPodProvider(lister, []PodMonitor{{
		Name: "vgpu", AcceleratorType: model.AcceleratorTypeVGPU, Namespaces: []string{"ok", "failed"},
		Selector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "vgpu"}},
		Port:     "9394", Path: "/metrics", Scheme: "http",
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
	provider, err := NewPodProvider(lister, []PodMonitor{{
		Name: "all-gpus", AcceleratorType: model.AcceleratorTypeGPU, AnyNamespace: true,
		Selector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "dcgm"}}, Port: "9400", Path: "/metrics", Scheme: "http",
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

func TestCompositeKeepsStaticTargetsOnDiscoveryError(t *testing.T) {
	t.Parallel()
	static := model.Source{Name: "static", Address: "http://host/metrics", AcceleratorType: model.AcceleratorTypeNPU}
	provider := Composite(
		Static([]model.Source{static}),
		providerFunc(func(context.Context) ([]model.Source, error) { return nil, errors.New("discovery failed") }),
	)
	targets, err := provider.Targets(context.Background())
	if err == nil || len(targets) != 1 || targets[0].Name != "static" {
		t.Fatalf("targets = %#v, error = %v", targets, err)
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
				{Addresses: []string{"10.1.0.2"}, Conditions: discoveryv1.EndpointConditions{Ready: &notReady}},
			},
		}},
	}
	provider, err := NewServiceProvider(lister, []ServiceMonitor{{
		Name: "npu-service", AcceleratorType: model.AcceleratorTypeNPU, Namespaces: []string{"npu"},
		Selector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "npu-exporter"}},
		Port:     portName, Path: "/metrics", Scheme: "http",
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
	if targets[0].Address != "http://10.1.0.1:8082/metrics" || targets[0].Labels["node"] != node || targets[0].AcceleratorType != model.AcceleratorTypeNPU {
		t.Fatalf("unexpected target: %#v", targets[0])
	}
}
