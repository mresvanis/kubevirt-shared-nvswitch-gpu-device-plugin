package deviceplugin

import (
	"context"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

type HealthChecker struct {
	deviceManager *DeviceManager
	checkInterval time.Duration
	healthHistory map[string][]HealthCheck
	logger        *logrus.Logger
	mutex         sync.RWMutex
	stopCh        chan struct{}
}

type HealthMonitor struct {
	checker *HealthChecker
	vfio    *VFIOManager
	logger  *logrus.Logger
}

func NewHealthChecker(deviceManager *DeviceManager, checkInterval time.Duration, logger *logrus.Logger) *HealthChecker {
	return &HealthChecker{
		deviceManager: deviceManager,
		checkInterval: checkInterval,
		healthHistory: make(map[string][]HealthCheck),
		logger:        logger,
		stopCh:        make(chan struct{}),
	}
}

func (hc *HealthChecker) Start(ctx context.Context) error {
	hc.logger.WithField("interval", hc.checkInterval).Info("Starting health checker")

	go hc.run(ctx)
	return nil
}

func (hc *HealthChecker) Stop() {
	hc.logger.Info("Stopping health checker")
	close(hc.stopCh)
}

func (hc *HealthChecker) run(ctx context.Context) {
	ticker := time.NewTicker(hc.checkInterval)
	defer ticker.Stop()

	vfioManager := NewVFIOManager(hc.logger)

	hc.performHealthCheck(vfioManager)

	for {
		select {
		case <-ticker.C:
			hc.performHealthCheck(vfioManager)
		case <-hc.stopCh:
			hc.logger.Info("Health checker stopped")
			return
		case <-ctx.Done():
			hc.logger.Info("Health checker context cancelled")
			return
		}
	}
}

func (hc *HealthChecker) performHealthCheck(vfioManager *VFIOManager) {
	hc.logger.Debug("Performing health check")

	hc.deviceManager.mutex.RLock()
	deviceIDs := make([]string, 0, len(hc.deviceManager.devices))
	for id := range hc.deviceManager.devices {
		deviceIDs = append(deviceIDs, id)
	}
	hc.deviceManager.mutex.RUnlock()

	for _, deviceID := range deviceIDs {
		hc.checkDeviceHealth(vfioManager, deviceID)
	}
}

func (hc *HealthChecker) checkDeviceHealth(vfioManager *VFIOManager, deviceID string) {
	start := time.Now()

	health, err := vfioManager.GetDeviceHealth(deviceID)
	if err != nil {
		hc.logger.WithFields(logrus.Fields{
			"device_id": deviceID,
			"error":     err,
		}).Warn("Health check failed")
		health = HealthFailed
	}

	if err := hc.deviceManager.UpdateDeviceHealth(deviceID, health); err != nil {
		hc.logger.WithFields(logrus.Fields{
			"device_id": deviceID,
			"error":     err,
		}).Error("Failed to update device health")
		return
	}

	status := hc.mapHealthToStatus(health)
	healthCheck := HealthCheck{
		DeviceID:  deviceID,
		Timestamp: time.Now(),
		Status:    status,
		Details:   hc.getHealthDetails(health, err),
	}

	hc.recordHealthCheck(healthCheck)

	duration := time.Since(start)
	hc.logger.WithFields(logrus.Fields{
		"device_id": deviceID,
		"health":    health,
		"duration":  duration,
	}).Debug("Health check completed")

	if health != HealthHealthy {
		hc.logger.WithFields(logrus.Fields{
			"device_id": deviceID,
			"health":    health,
			"details":   healthCheck.Details,
		}).Warn("Device health issue detected")
	}
}

func (hc *HealthChecker) mapHealthToStatus(health DeviceHealth) HealthStatus {
	switch health {
	case HealthHealthy:
		return HealthStatusHealthy
	case HealthUnhealthy:
		return HealthStatusUnhealthy
	case HealthFailed:
		return HealthStatusFailed
	default:
		return HealthStatusUnknown
	}
}

func (hc *HealthChecker) getHealthDetails(health DeviceHealth, err error) string {
	switch health {
	case HealthHealthy:
		return "Device is healthy"
	case HealthUnhealthy:
		return "Device is unhealthy"
	case HealthFailed:
		if err != nil {
			return err.Error()
		}
		return "Device has failed"
	default:
		return "Health status unknown"
	}
}

func (hc *HealthChecker) recordHealthCheck(check HealthCheck) {
	hc.mutex.Lock()
	defer hc.mutex.Unlock()

	const maxHistorySize = 100

	if hc.healthHistory[check.DeviceID] == nil {
		hc.healthHistory[check.DeviceID] = make([]HealthCheck, 0, maxHistorySize)
	}

	history := hc.healthHistory[check.DeviceID]

	if len(history) >= maxHistorySize {
		copy(history, history[1:])
		history = history[:maxHistorySize-1]
	}

	history = append(history, check)
	hc.healthHistory[check.DeviceID] = history
}

func (hc *HealthChecker) GetHealthHistory(deviceID string) []HealthCheck {
	hc.mutex.RLock()
	defer hc.mutex.RUnlock()

	history, exists := hc.healthHistory[deviceID]
	if !exists {
		return []HealthCheck{}
	}

	result := make([]HealthCheck, len(history))
	copy(result, history)
	return result
}

func (hc *HealthChecker) GetCurrentHealth(deviceID string) (HealthStatus, string) {
	hc.mutex.RLock()
	defer hc.mutex.RUnlock()

	history, exists := hc.healthHistory[deviceID]
	if !exists || len(history) == 0 {
		return HealthStatusUnknown, "No health data available"
	}

	latest := history[len(history)-1]
	return latest.Status, latest.Details
}

func (hc *HealthChecker) GetOverallHealth() map[string]interface{} {
	hc.mutex.RLock()
	defer hc.mutex.RUnlock()

	totalDevices := 0
	healthyDevices := 0
	unhealthyDevices := 0
	failedDevices := 0
	unknownDevices := 0

	for deviceID := range hc.healthHistory {
		totalDevices++

		history := hc.healthHistory[deviceID]
		if len(history) == 0 {
			unknownDevices++
			continue
		}

		latest := history[len(history)-1]
		switch latest.Status {
		case HealthStatusHealthy:
			healthyDevices++
		case HealthStatusUnhealthy:
			unhealthyDevices++
		case HealthStatusFailed:
			failedDevices++
		default:
			unknownDevices++
		}
	}

	isHealthy := failedDevices == 0 && unhealthyDevices == 0 && totalDevices > 0

	return map[string]interface{}{
		"healthy":           isHealthy,
		"total_devices":     totalDevices,
		"healthy_devices":   healthyDevices,
		"unhealthy_devices": unhealthyDevices,
		"failed_devices":    failedDevices,
		"unknown_devices":   unknownDevices,
		"timestamp":         time.Now().Format(time.RFC3339),
	}
}

func (hc *HealthChecker) IsDeviceHealthy(deviceID string) bool {
	status, _ := hc.GetCurrentHealth(deviceID)
	return status == HealthStatusHealthy
}

func (hc *HealthChecker) GetUnhealthyDevices() []string {
	hc.mutex.RLock()
	defer hc.mutex.RUnlock()

	var unhealthy []string

	for deviceID, history := range hc.healthHistory {
		if len(history) == 0 {
			continue
		}

		latest := history[len(history)-1]
		if latest.Status != HealthStatusHealthy {
			unhealthy = append(unhealthy, deviceID)
		}
	}

	return unhealthy
}

func NewHealthMonitor(deviceManager *DeviceManager, logger *logrus.Logger) *HealthMonitor {
	checker := NewHealthChecker(deviceManager, DefaultHealthCheckInterval, logger)
	vfio := NewVFIOManager(logger)

	return &HealthMonitor{
		checker: checker,
		vfio:    vfio,
		logger:  logger,
	}
}

func (hm *HealthMonitor) Start(ctx context.Context) error {
	return hm.checker.Start(ctx)
}

func (hm *HealthMonitor) Stop() {
	hm.checker.Stop()
}

func (hm *HealthMonitor) GetHealthStatus() map[string]interface{} {
	return hm.checker.GetOverallHealth()
}

func (hm *HealthMonitor) IsHealthy() bool {
	status := hm.checker.GetOverallHealth()
	healthy, ok := status["healthy"].(bool)
	return ok && healthy
}
