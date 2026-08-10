package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	typeddiscoveryv1 "k8s.io/client-go/kubernetes/typed/discovery/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"xiaoshiai.cn/rune/internal/config"
	"xiaoshiai.cn/rune/internal/discovery"
	"xiaoshiai.cn/rune/internal/exporter"
	"xiaoshiai.cn/rune/internal/scrape"
)

func main() {
	if err := run(); err != nil {
		slog.Error("accelerator-exporter stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	var configPath string
	flag.StringVar(&configPath, "config", "", "Path to the accelerator exporter YAML configuration")
	flag.Parse()
	if configPath == "" {
		return fmt.Errorf("--config is required")
	}
	runtimeConfig, err := config.Load(configPath)
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)
	providers := []discovery.Provider{}
	if len(runtimeConfig.Sources) > 0 {
		providers = append(providers, discovery.Static(runtimeConfig.Sources))
	}
	if len(runtimeConfig.PodMonitors) > 0 || len(runtimeConfig.ServiceMonitors) > 0 {
		restConfig, err := kubernetesConfig(runtimeConfig.Kubeconfig)
		if err != nil {
			return err
		}
		coreClient, err := typedcorev1.NewForConfig(restConfig)
		if err != nil {
			return fmt.Errorf("create Kubernetes CoreV1 client: %w", err)
		}
		if len(runtimeConfig.PodMonitors) > 0 {
			provider, err := discovery.NewPodProvider(discovery.NewCorePodLister(coreClient), runtimeConfig.PodMonitors)
			if err != nil {
				return err
			}
			providers = append(providers, provider)
		}
		if len(runtimeConfig.ServiceMonitors) > 0 {
			discoveryClient, err := typeddiscoveryv1.NewForConfig(restConfig)
			if err != nil {
				return fmt.Errorf("create Kubernetes DiscoveryV1 client: %w", err)
			}
			provider, err := discovery.NewServiceProvider(
				discovery.NewServiceEndpointLister(coreClient, discoveryClient), runtimeConfig.ServiceMonitors,
			)
			if err != nil {
				return err
			}
			providers = append(providers, provider)
		}
	}
	httpClient := &http.Client{Timeout: runtimeConfig.ScrapeTimeout}
	options := exporter.Options{
		ScrapeInterval: runtimeConfig.ScrapeInterval,
		ScrapeTimeout:  runtimeConfig.ScrapeTimeout,
		MaxConcurrency: runtimeConfig.MaxConcurrency,
		TargetProvider: discovery.Composite(providers...),
	}
	metricExporter, err := exporter.New(scrape.NewClient(httpClient), options, logger)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go metricExporter.Run(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", metricExporter.Metrics)
	mux.HandleFunc("GET /healthz", metricExporter.Health)
	mux.HandleFunc("GET /readyz", metricExporter.Ready)
	server := &http.Server{
		Addr:              runtimeConfig.ListenAddress,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("starting accelerator exporter", "address", runtimeConfig.ListenAddress, "sourceDefinitions", len(runtimeConfig.Sources)+len(runtimeConfig.PodMonitors)+len(runtimeConfig.ServiceMonitors))
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
