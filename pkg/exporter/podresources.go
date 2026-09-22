// This file reads the device allocation facts of the local node from
// the kubelet pod-resources API.
//
// The allocation fact is the authority on which Pod owns which device, which is
// why the relation layer is built from it rather than from whatever workload
// labels a vendor exporter happens to attach to a metric.
package exporter

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials/insecure"
	podresourcesv1 "k8s.io/kubelet/pkg/apis/podresources/v1"

	"xiaoshiai.cn/accelerator-exporter/pkg/accelerator"
)

// DefaultSocketPath is where kubelet exposes the pod-resources API. The
// directory has to be mounted into the exporter container, read-only.
const DefaultSocketPath = "/var/lib/kubelet/pod-resources/kubelet.sock"

// defaultCallTimeout bounds a hung kubelet so it cannot stall the collection
// loop. It bounds the call, which is what a stalled kubelet looks like.
const defaultCallTimeout = 10 * time.Second

// maxReconnectDelay bounds how long gRPC waits between attempts to reach
// kubelet. grpc's default is two minutes, which is far longer than a collection
// round: a kubelet that came back after a long outage could stay unreachable for
// another two minutes while the channel sat out its timer. Capping the delay at
// the collection period makes recovery cost at most one round.
const maxReconnectDelay = 15 * time.Second

// minConnectTimeout bounds a single connection attempt, so one stalled attempt
// cannot outlive the round that started it.
const minConnectTimeout = 5 * time.Second

// Reader reads the allocation facts of the node it runs on.
//
// The kubelet connection is established once and reused: reconnecting on every
// read would pay a dial per collection round for nothing, because a socket that
// went away makes the next call fail on its own. What is never reused is the
// answer — each read reports the allocations kubelet has at that moment.
type PodResourcesReader struct {
	socketPath string
	timeout    time.Duration
	metadata   PodMetadata

	mu         sync.Mutex
	connection *grpc.ClientConn
}

// NewReader creates a reader for one kubelet pod-resources socket.
func NewPodResourcesReader(socketPath string, metadata PodMetadata) *PodResourcesReader {
	if socketPath == "" {
		socketPath = DefaultSocketPath
	}
	return &PodResourcesReader{socketPath: socketPath, timeout: defaultCallTimeout, metadata: metadata}
}

// Close releases the kubelet connection.
func (r *PodResourcesReader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.connection == nil {
		return nil
	}
	connection := r.connection
	r.connection = nil
	return connection.Close()
}

// Allocations returns one fact per allocated observation target.
//
// A kubelet that cannot be reached, or that refuses the call, returns an error
// and no facts: the caller must report that rather than reuse an earlier answer,
// because an empty relation layer and a broken allocation source are different
// things.
//
// An allocation whose Pod UID cannot be resolved is reported as an error and
// omitted, because pod_uid is what separates two Pods that share a name.
func (r *PodResourcesReader) Allocations(ctx context.Context) ([]accelerator.Allocation, error) {
	response, err := r.list(ctx)
	if err != nil {
		return nil, err
	}
	// resolved deduplicates the Pod metadata lookups of this single read.
	resolved := map[string]string{}
	var allocations []accelerator.Allocation
	var allocationErrors []error
	for _, pod := range response.GetPodResources() {
		if pod.GetName() == "" || pod.GetNamespace() == "" {
			continue
		}
		uid, err := r.podUID(ctx, pod.GetNamespace(), pod.GetName(), resolved)
		if err != nil {
			allocationErrors = append(allocationErrors, err)
			continue
		}
		for _, container := range pod.GetContainers() {
			for _, devices := range container.GetDevices() {
				for _, deviceID := range devices.GetDeviceIds() {
					allocations = append(allocations, accelerator.Allocation{
						Namespace:      pod.GetNamespace(),
						Pod:            pod.GetName(),
						PodUID:         uid,
						Container:      container.GetName(),
						NativeResource: devices.GetResourceName(),
						NativeDeviceID: deviceID,
					})
				}
			}
		}
	}
	return allocations, errors.Join(allocationErrors...)
}

func (r *PodResourcesReader) list(ctx context.Context) (*podresourcesv1.ListPodResourcesResponse, error) {
	connection, err := r.connect()
	if err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	response, err := podresourcesv1.NewPodResourcesListerClient(connection).List(
		callCtx, &podresourcesv1.ListPodResourcesRequest{},
	)
	if err != nil {
		// kubelet is called once per collection round, so the reconnect backoff
		// the channel accumulated between rounds is pure delay: without waking it
		// up, a kubelet that came back is only noticed when its next scheduled
		// attempt falls due, which can be a minute or more away. Resetting the
		// backoff makes the following round reconnect immediately instead.
		connection.ResetConnectBackoff()
		return nil, fmt.Errorf("list kubelet pod resources from %s: %w", r.socketPath, err)
	}
	return response, nil
}

// connect returns the shared kubelet connection, establishing it on first use.
// The connection needs no liveness check of its own: a channel whose socket is
// gone fails the next call, and gRPC reconnects underneath with backoff.
func (r *PodResourcesReader) connect() (*grpc.ClientConn, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.connection != nil {
		return r.connection, nil
	}
	// backoff.DefaultConfig is the documented base for overriding a subset of the
	// reconnect backoff. grpc does not fill in omitted fields, and a zero
	// Multiplier would collapse the delay to zero and spin.
	reconnect := backoff.DefaultConfig
	reconnect.MaxDelay = maxReconnectDelay

	connection, err := grpc.NewClient(
		"unix://"+r.socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", r.socketPath)
		}),
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff:           reconnect,
			MinConnectTimeout: minConnectTimeout,
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("dial kubelet pod resources socket %s: %w", r.socketPath, err)
	}
	r.connection = connection
	return connection, nil
}

// podUID resolves a Pod UID, reusing the answer within one read only: a Pod's
// UID cannot change while the read is in flight, but it must not be carried into
// the next round, where the Pod may have been replaced.
func (r *PodResourcesReader) podUID(ctx context.Context, namespace, name string, cache map[string]string) (string, error) {
	if r.metadata == nil {
		return "", fmt.Errorf("no Pod metadata source configured for Pod %s/%s", namespace, name)
	}
	key := namespace + "/" + name
	if uid, ok := cache[key]; ok {
		return uid, nil
	}
	uid, err := r.metadata.PodUID(ctx, namespace, name)
	if err != nil {
		return "", err
	}
	cache[key] = uid
	return uid, nil
}
