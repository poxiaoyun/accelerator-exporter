package exporter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	typeddiscoveryv1 "k8s.io/client-go/kubernetes/typed/discovery/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"xiaoshiai.cn/accelerator-exporter/pkg/nodefacts"
	"xiaoshiai.cn/accelerator-exporter/pkg/scrapetargets"
)

// Run wires every external system this process needs and serves until the
// context ends.
//
// Initialization lives here rather than in the command, so the entry point stays
// a declaration of the program — options in, run out — and Options never has to
// carry an instance.
func Run(ctx context.Context, options *Options) error {
	if err := options.Validate(); err != nil {
		return err
	}
	nodeName := strings.TrimSpace(options.NodeName)
	if nodeName == "" {
		nodeName = strings.TrimSpace(os.Getenv("NODE_NAME"))
	}
	if nodeName == "" {
		return fmt.Errorf("node name is required: set nodeName or the NODE_NAME environment variable")
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	restConfiguration, err := kubernetesConfig(options.Kubeconfig)
	if err != nil {
		return err
	}
	coreClient, err := typedcorev1.NewForConfig(restConfiguration)
	if err != nil {
		return fmt.Errorf("create Kubernetes CoreV1 client: %w", err)
	}
	discoveryClient, err := typeddiscoveryv1.NewForConfig(restConfiguration)
	if err != nil {
		return fmt.Errorf("create Kubernetes DiscoveryV1 client: %w", err)
	}

	// Both providers are built unconditionally: neither does any I/O until it is
	// asked for targets, and a provider whose sources use the other monitor kind
	// simply resolves nothing.
	podProvider, err := scrapetargets.NewPodProvider(
		scrapetargets.NewCorePodLister(coreClient), nodeName, options.Sources,
	)
	if err != nil {
		return err
	}
	serviceProvider, err := scrapetargets.NewServiceProvider(
		scrapetargets.NewServiceEndpointLister(coreClient, discoveryClient), nodeName, options.Sources,
	)
	if err != nil {
		return err
	}

	// The allocation source is wired unconditionally too. Whether kubelet answers
	// is observed on every round and reported through
	// accelerator_exporter_allocation_source_up, so a socket that is missing at
	// startup does not rule the relation layer out for the life of the process.
	allocations := NewPodResourcesReader(options.PodResourcesSocket, NewPodMetadataReader(
		func(namespace string) PodGetter { return coreClient.Pods(namespace) },
	))
	defer func() { _ = allocations.Close() }()
	logger.Info("reading device allocation facts from kubelet", "socket", options.PodResourcesSocket)

	metricExporter, err := New(
		options,
		NewHTTPScraper(&http.Client{Timeout: options.ScrapeTimeout}),
		scrapetargets.Composite(options.Sources, podProvider, serviceProvider),
		allocations,
		logger,
	)
	if err != nil {
		return err
	}

	go metricExporter.Run(ctx)
	// The same DaemonSet publishes the node's CPU model, which Cloud's node
	// resource discovery reads back off the Node object.
	go nodefacts.NewCPUModelPublisher(coreClient.Nodes(), nodeName, logger).Run(ctx)
	if options.DiskDiscovery.Enabled {
		disks := nodefacts.NewDiskPublisher(coreClient.Nodes(), nodeName, nodefacts.HostDiskDetector{Root: options.DiskDiscovery.HostRoot}, logger)
		disks.SetInterval(options.DiskDiscovery.Interval)
		go disks.Run(ctx)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", metricExporter.Metrics)
	mux.HandleFunc("GET /healthz", metricExporter.Health)
	mux.HandleFunc("GET /readyz", metricExporter.Ready)
	server := &http.Server{
		Addr:              options.ListenAddress,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	serverErrors := make(chan error, 1)
	go func() {
		logger.Info(
			"starting accelerator exporter",
			"address", options.ListenAddress, "node", nodeName, "sources", len(options.Sources),
		)
		serverErrors <- server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shut down HTTP server: %w", err)
		}
		return nil
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve HTTP: %w", err)
	}
}

func kubernetesConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig != "" {
		configuration, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, fmt.Errorf("load kubeconfig %q: %w", kubeconfig, err)
		}
		return configuration, nil
	}
	configuration, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("load in-cluster Kubernetes config: %w", err)
	}
	return configuration, nil
}
