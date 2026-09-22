// Command accelerator-exporter collects the accelerator metrics of the Node it
// runs on and serves them as the normalized accelerator_* contract.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"xiaoshiai.cn/accelerator-exporter/pkg/exporter"
)

func main() {
	if err := run(); err != nil {
		slog.Error("accelerator-exporter stopped", "error", err)
		os.Exit(1)
	}
}

// run is the whole command: read the options, then hand them to the package that
// owns both them and their initialization.
func run() error {
	var configPath string
	flag.StringVar(&configPath, "config", "", "Path to the accelerator exporter YAML configuration")
	flag.Parse()

	options, err := exporter.LoadOptions(configPath)
	if err != nil {
		return err
	}

	// The first SIGINT or SIGTERM cancels the context for a graceful shutdown;
	// a second one exits immediately.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return exporter.Run(ctx, options)
}
