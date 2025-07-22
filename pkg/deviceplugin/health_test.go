package deviceplugin

import (
	"testing"
	"time"

	"github.com/sirupsen/logrus/hooks/test"
)

func TestNewHealthChecker(t *testing.T) {
	logger, _ := test.NewNullLogger()

	dm := &DeviceManager{
		devices: make(map[string]*GPUDevice),
		logger:  logger,
	}

	hc := NewHealthChecker(dm, time.Second, logger)

	if hc.deviceManager != dm {
		t.Errorf("Expected device manager to be set correctly")
	}

	if hc.checkInterval != time.Second {
		t.Errorf("Expected check interval to be 1 second, got %v", hc.checkInterval)
	}

	if hc.logger != logger {
		t.Errorf("Expected logger to be set correctly")
	}

	if hc.healthHistory == nil {
		t.Errorf("Expected health history to be initialized")
	}
}

func TestHealthChecker_recordHealthCheck(t *testing.T) {
	logger, _ := test.NewNullLogger()

	dm := &DeviceManager{
		devices: make(map[string]*GPUDevice),
		logger:  logger,
	}

	hc := NewHealthChecker(dm, time.Second, logger)

	check := HealthCheck{
		DeviceID:  "test-device",
		Timestamp: time.Now(),
		Status:    HealthStatusHealthy,
		Details:   "Device is healthy",
	}

	hc.recordHealthCheck(check)

	history := hc.GetHealthHistory("test-device")
	if len(history) != 1 {
		t.Errorf("Expected 1 health check in history, got %d", len(history))
	}

	if history[0].DeviceID != "test-device" {
		t.Errorf("Expected device ID test-device, got %s", history[0].DeviceID)
	}

	if history[0].Status != HealthStatusHealthy {
		t.Errorf("Expected status HealthStatusHealthy, got %v", history[0].Status)
	}
}

func TestHealthChecker_GetCurrentHealth(t *testing.T) {
	logger, _ := test.NewNullLogger()

	dm := &DeviceManager{
		devices: make(map[string]*GPUDevice),
		logger:  logger,
	}

	hc := NewHealthChecker(dm, time.Second, logger)

	status, _ := hc.GetCurrentHealth("nonexistent")
	if status != HealthStatusUnknown {
		t.Errorf("Expected HealthStatusUnknown for nonexistent device, got %v", status)
	}

	check := HealthCheck{
		DeviceID:  "test-device",
		Timestamp: time.Now(),
		Status:    HealthStatusHealthy,
		Details:   "Device is healthy",
	}
	hc.recordHealthCheck(check)

	status, details := hc.GetCurrentHealth("test-device")
	if status != HealthStatusHealthy {
		t.Errorf("Expected HealthStatusHealthy, got %v", status)
	}

	if details != "Device is healthy" {
		t.Errorf("Expected 'Device is healthy', got %s", details)
	}
}

func TestHealthChecker_mapHealthToStatus(t *testing.T) {
	logger, _ := test.NewNullLogger()

	dm := &DeviceManager{
		devices: make(map[string]*GPUDevice),
		logger:  logger,
	}

	hc := NewHealthChecker(dm, time.Second, logger)

	tests := []struct {
		health   DeviceHealth
		expected HealthStatus
	}{
		{HealthHealthy, HealthStatusHealthy},
		{HealthUnhealthy, HealthStatusUnhealthy},
		{HealthFailed, HealthStatusFailed},
		{HealthUnknown, HealthStatusUnknown},
	}

	for _, test := range tests {
		result := hc.mapHealthToStatus(test.health)
		if result != test.expected {
			t.Errorf("mapHealthToStatus(%v) = %v, expected %v", test.health, result, test.expected)
		}
	}
}

func TestHealthChecker_getHealthDetails(t *testing.T) {
	logger, _ := test.NewNullLogger()

	dm := &DeviceManager{
		devices: make(map[string]*GPUDevice),
		logger:  logger,
	}

	hc := NewHealthChecker(dm, time.Second, logger)

	tests := []struct {
		health   DeviceHealth
		err      error
		expected string
	}{
		{HealthHealthy, nil, "Device is healthy"},
		{HealthUnhealthy, nil, "Device is unhealthy"},
		{HealthFailed, nil, "Device has failed"},
		{HealthUnknown, nil, "Health status unknown"},
	}

	for _, test := range tests {
		result := hc.getHealthDetails(test.health, test.err)
		if result != test.expected {
			t.Errorf("getHealthDetails(%v, %v) = %s, expected %s", test.health, test.err, result, test.expected)
		}
	}
}

func TestHealthChecker_GetOverallHealth(t *testing.T) {
	logger, _ := test.NewNullLogger()

	dm := &DeviceManager{
		devices: make(map[string]*GPUDevice),
		logger:  logger,
	}

	hc := NewHealthChecker(dm, time.Second, logger)

	check1 := HealthCheck{
		DeviceID:  "device1",
		Timestamp: time.Now(),
		Status:    HealthStatusHealthy,
		Details:   "Device is healthy",
	}
	check2 := HealthCheck{
		DeviceID:  "device2",
		Timestamp: time.Now(),
		Status:    HealthStatusUnhealthy,
		Details:   "Device is unhealthy",
	}

	hc.recordHealthCheck(check1)
	hc.recordHealthCheck(check2)

	overall := hc.GetOverallHealth()

	if overall["total_devices"] != 2 {
		t.Errorf("Expected 2 total devices, got %v", overall["total_devices"])
	}

	if overall["healthy_devices"] != 1 {
		t.Errorf("Expected 1 healthy device, got %v", overall["healthy_devices"])
	}

	if overall["unhealthy_devices"] != 1 {
		t.Errorf("Expected 1 unhealthy device, got %v", overall["unhealthy_devices"])
	}

	if overall["healthy"].(bool) {
		t.Errorf("Expected overall health to be false")
	}
}

func TestHealthChecker_IsDeviceHealthy(t *testing.T) {
	logger, _ := test.NewNullLogger()

	dm := &DeviceManager{
		devices: make(map[string]*GPUDevice),
		logger:  logger,
	}

	hc := NewHealthChecker(dm, time.Second, logger)

	if hc.IsDeviceHealthy("nonexistent") {
		t.Errorf("Expected nonexistent device to not be healthy")
	}

	check := HealthCheck{
		DeviceID:  "test-device",
		Timestamp: time.Now(),
		Status:    HealthStatusHealthy,
		Details:   "Device is healthy",
	}
	hc.recordHealthCheck(check)

	if !hc.IsDeviceHealthy("test-device") {
		t.Errorf("Expected healthy device to be healthy")
	}

	unhealthyCheck := HealthCheck{
		DeviceID:  "unhealthy-device",
		Timestamp: time.Now(),
		Status:    HealthStatusUnhealthy,
		Details:   "Device is unhealthy",
	}
	hc.recordHealthCheck(unhealthyCheck)

	if hc.IsDeviceHealthy("unhealthy-device") {
		t.Errorf("Expected unhealthy device to not be healthy")
	}
}

func TestHealthChecker_GetUnhealthyDevices(t *testing.T) {
	logger, _ := test.NewNullLogger()

	dm := &DeviceManager{
		devices: make(map[string]*GPUDevice),
		logger:  logger,
	}

	hc := NewHealthChecker(dm, time.Second, logger)

	checks := []HealthCheck{
		{
			DeviceID:  "healthy-device",
			Timestamp: time.Now(),
			Status:    HealthStatusHealthy,
			Details:   "Device is healthy",
		},
		{
			DeviceID:  "unhealthy-device",
			Timestamp: time.Now(),
			Status:    HealthStatusUnhealthy,
			Details:   "Device is unhealthy",
		},
		{
			DeviceID:  "failed-device",
			Timestamp: time.Now(),
			Status:    HealthStatusFailed,
			Details:   "Device has failed",
		},
	}

	for _, check := range checks {
		hc.recordHealthCheck(check)
	}

	unhealthy := hc.GetUnhealthyDevices()

	if len(unhealthy) != 2 {
		t.Errorf("Expected 2 unhealthy devices, got %d", len(unhealthy))
	}

	expectedUnhealthy := map[string]bool{
		"unhealthy-device": true,
		"failed-device":    true,
	}

	for _, deviceID := range unhealthy {
		if !expectedUnhealthy[deviceID] {
			t.Errorf("Unexpected unhealthy device: %s", deviceID)
		}
	}
}

func TestNewHealthMonitor(t *testing.T) {
	logger, _ := test.NewNullLogger()

	dm := &DeviceManager{
		devices: make(map[string]*GPUDevice),
		logger:  logger,
	}

	hm := NewHealthMonitor(dm, logger)

	if hm.checker == nil {
		t.Errorf("Expected health checker to be initialized")
	}

	if hm.vfio == nil {
		t.Errorf("Expected VFIO manager to be initialized")
	}

	if hm.logger != logger {
		t.Errorf("Expected logger to be set correctly")
	}
}

func TestHealthMonitor_IsHealthy(t *testing.T) {
	logger, _ := test.NewNullLogger()

	dm := &DeviceManager{
		devices: make(map[string]*GPUDevice),
		logger:  logger,
	}

	hm := NewHealthMonitor(dm, logger)

	check := HealthCheck{
		DeviceID:  "healthy-device",
		Timestamp: time.Now(),
		Status:    HealthStatusHealthy,
		Details:   "Device is healthy",
	}
	hm.checker.recordHealthCheck(check)

	if !hm.IsHealthy() {
		t.Errorf("Expected health monitor to report healthy")
	}

	unhealthyCheck := HealthCheck{
		DeviceID:  "unhealthy-device",
		Timestamp: time.Now(),
		Status:    HealthStatusUnhealthy,
		Details:   "Device is unhealthy",
	}
	hm.checker.recordHealthCheck(unhealthyCheck)

	if hm.IsHealthy() {
		t.Errorf("Expected health monitor to report unhealthy")
	}
}
