package exporter

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PodMetadata resolves the metadata a device allocation fact does not carry.
type PodMetadata interface {
	PodUID(ctx context.Context, namespace, name string) (string, error)
}

// PodGetter is the narrow Kubernetes seam required to read Pod metadata.
type PodGetter interface {
	Get(ctx context.Context, name string, options metav1.GetOptions) (*corev1.Pod, error)
}

// PodGetters returns the Pod client of one namespace.
type PodGetters func(namespace string) PodGetter

// PodMetadataReader resolves Pod UIDs through the Kubernetes API.
type PodMetadataReader struct {
	pods PodGetters
}

// NewPodMetadataReader creates a Pod UID resolver.
func NewPodMetadataReader(pods PodGetters) *PodMetadataReader {
	return &PodMetadataReader{pods: pods}
}

// PodUID returns the UID of one Pod. The allocation fact names the Pod but not
// its UID, and the relation layer needs the UID to tell two Pods that share a
// name apart.
func (reader *PodMetadataReader) PodUID(ctx context.Context, namespace, name string) (string, error) {
	if reader.pods == nil {
		return "", fmt.Errorf("no Pod client configured")
	}
	pod, err := reader.pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("get Pod %s/%s: %w", namespace, name, err)
	}
	if pod.UID == "" {
		return "", fmt.Errorf("Pod %s/%s has no UID", namespace, name)
	}
	return string(pod.UID), nil
}
