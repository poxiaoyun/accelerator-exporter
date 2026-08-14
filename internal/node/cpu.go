// Package node publishes node-local hardware information to Kubernetes Nodes.
package node

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/klauspost/cpuid/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	CPUModelLabel           = "feature.node.cloud.xiaoshiai.cn/cpu-model.name"
	CPUModelPredefinedLabel = "feature.node.cloud.xiaoshiai.cn/cpu-model.predefined-name"
	CPUModelRawAnnotation   = "feature.node.cloud.xiaoshiai.cn/cpu-model.raw-name"

	updateInterval = 30 * time.Minute
	retryInterval  = time.Minute
)

type nodeClient interface {
	Get(ctx context.Context, name string, opts metav1.GetOptions) (*corev1.Node, error)
	Patch(ctx context.Context, name string, patchType types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (*corev1.Node, error)
}

type cpuDetector func() (brandName, vendorName string)

// CPUModelPublisher detects the local CPU and publishes it on this Pod's Node.
type CPUModelPublisher struct {
	nodes      nodeClient
	nodeName   string
	logger     *slog.Logger
	detectCPU  cpuDetector
	updateWait time.Duration
	retryWait  time.Duration
}

func NewCPUModelPublisher(nodes nodeClient, nodeName string, logger *slog.Logger) *CPUModelPublisher {
	return &CPUModelPublisher{
		nodes:    nodes,
		nodeName: nodeName,
		logger:   logger,
		detectCPU: func() (string, string) {
			return cpuid.CPU.BrandName, cpuid.CPU.VendorString
		},
		updateWait: updateInterval,
		retryWait:  retryInterval,
	}
}

// Run publishes immediately, refreshes periodically, and retries transient failures.
func (publisher *CPUModelPublisher) Run(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			wait := publisher.updateWait
			if err := publisher.Publish(ctx); err != nil {
				publisher.logger.Error("publish node CPU model", "node", publisher.nodeName, "error", err)
				wait = publisher.retryWait
			}
			timer.Reset(wait)
		}
	}
}

// Publish reconciles the CPU model label and raw-name annotation on one Node.
func (publisher *CPUModelPublisher) Publish(ctx context.Context) error {
	kubernetesNode, err := publisher.nodes.Get(ctx, publisher.nodeName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get node %q: %w", publisher.nodeName, err)
	}

	brandName, vendorName := publisher.detectCPU()
	rawModel := resolveCPUModel(kubernetesNode, brandName, vendorName)
	model := EncodeCPUModelName(rawModel)
	if model == "" {
		rawModel = "Generic"
		model = rawModel
	}
	if kubernetesNode.Labels[CPUModelLabel] == model && kubernetesNode.Annotations[CPUModelRawAnnotation] == rawModel {
		return nil
	}

	patch, err := json.Marshal(map[string]any{
		"metadata": map[string]any{
			"labels": map[string]string{
				CPUModelLabel: model,
			},
			"annotations": map[string]string{
				CPUModelRawAnnotation: rawModel,
			},
		},
	})
	if err != nil {
		return fmt.Errorf("marshal node CPU model patch: %w", err)
	}
	if _, err := publisher.nodes.Patch(ctx, publisher.nodeName, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("patch node %q CPU model: %w", publisher.nodeName, err)
	}
	publisher.logger.Info("published node CPU model", "node", publisher.nodeName, "rawModel", rawModel, "model", model)
	return nil
}

func resolveCPUModel(kubernetesNode *corev1.Node, brandName, vendorName string) string {
	if brandName = strings.TrimSpace(brandName); brandName != "" {
		return brandName
	}
	if kubernetesNode != nil {
		if predefined := strings.TrimSpace(kubernetesNode.Labels[CPUModelPredefinedLabel]); predefined != "" {
			return predefined
		}
	}
	if vendorName = strings.TrimSpace(vendorName); vendorName != "" {
		return vendorName
	}
	return "Generic"
}

// EncodeCPUModelName converts a CPU brand string into a Kubernetes label value.
func EncodeCPUModelName(raw string) string {
	if before, _, found := strings.Cut(raw, "@"); found {
		raw = before
	}
	raw = strings.NewReplacer(
		"(R)", "", "(r)", "", "(TM)", "", "(tm)", "", "(C)", "", "(c)", "",
		"®", "", "™", "", "©", "",
	).Replace(raw)

	var builder strings.Builder
	separator := false
	for _, character := range raw {
		if isASCIILetter(character) || isASCIIDigit(character) || character == '.' || character == '_' {
			if separator && builder.Len() > 0 {
				builder.WriteByte('-')
			}
			builder.WriteRune(character)
			separator = false
			continue
		}
		separator = true
	}
	encoded := strings.Trim(builder.String(), "-._")
	if len(encoded) > 63 {
		encoded = strings.TrimRight(encoded[:63], "-._")
	}
	return encoded
}

func isASCIILetter(character rune) bool {
	return character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z'
}

func isASCIIDigit(character rune) bool {
	return character >= '0' && character <= '9'
}
