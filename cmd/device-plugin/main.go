package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mresvanis/shared-nvswitch-gpu-device-plugin/pkg/deviceplugin"
	"github.com/mresvanis/shared-nvswitch-gpu-device-plugin/pkg/logging"
)

var (
	socketPath = flag.String("socket-path", deviceplugin.DevicePluginPath, "Device plugin socket directory")

	resourceName = flag.String("resource-name", deviceplugin.DefaultResourceName, "Resource name prefix")

	logLevel = flag.String("log-level", "info", "Log level (debug, info, warn, error)")

	configPath = flag.String("config-path", "/etc/shared-nvswitch-device-plugin/config.yaml", "Configuration file path")
)

func main() {
	flag.Parse()

	logger := logging.NewLogger(*logLevel)
	logger.Info("Starting NVIDIA Shared NVSwitch GPU Device Plugin")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dp, err := deviceplugin.New(&deviceplugin.Config{
		SocketPath:   *socketPath,
		ResourceName: *resourceName,
		ConfigPath:   *configPath,
		Logger:       logger,
	})
	if err != nil {
		logger.WithError(err).Fatal("Failed to create device plugin")
	}

	if err := dp.Start(ctx); err != nil {
		logger.WithError(err).Fatal("Failed to start device plugin")
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigChan:
		logger.WithField("signal", sig).Info("Received shutdown signal")
	case <-ctx.Done():
		logger.Info("Context cancelled")
	}

	logger.Info("Shutting down device plugin")
	dp.Stop()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	<-shutdownCtx.Done()
	logger.Warn("Forced shutdown after timeout")
}
