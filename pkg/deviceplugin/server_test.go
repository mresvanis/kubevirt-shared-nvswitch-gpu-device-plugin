package deviceplugin

import (
	"context"
	"testing"

	"github.com/sirupsen/logrus/hooks/test"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

func TestNew(t *testing.T) {
	logger, _ := test.NewNullLogger()

	tests := []struct {
		name    string
		config  *Config
		wantErr bool
	}{
		{
			name:    "nil config",
			config:  nil,
			wantErr: true,
		},
		{
			name: "nil logger",
			config: &Config{
				SocketPath:   "/tmp",
				ResourceName: "test",
				ConfigPath:   "/tmp/config",
			},
			wantErr: true,
		},
		{
			name: "valid config",
			config: &Config{
				SocketPath:   "/tmp",
				ResourceName: "nvidia.com/gpu-h200-1x",
				ConfigPath:   "/tmp/config",
				Logger:       logger,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dp, err := New(tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("New() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && dp == nil {
				t.Errorf("New() returned nil device plugin")
			}
		})
	}
}

func TestDevicePluginService_GetDevicePluginOptions(t *testing.T) {
	logger, _ := test.NewNullLogger()

	service := &DevicePluginService{
		logger: logger,
	}

	opts, err := service.GetDevicePluginOptions(context.Background(), &pluginapi.Empty{})
	if err != nil {
		t.Errorf("GetDevicePluginOptions() error = %v", err)
		return
	}

	if opts.PreStartRequired {
		t.Errorf("Expected PreStartRequired to be false")
	}

	if !opts.GetPreferredAllocationAvailable {
		t.Errorf("Expected GetPreferredAllocationAvailable to be true")
	}
}

func TestDevicePluginService_getAvailableDevices(t *testing.T) {
	logger, _ := test.NewNullLogger()

	deviceManager := &DeviceManager{
		devices: make(map[string]*GPUDevice),
		logger:  logger,
	}

	service := &DevicePluginService{
		deviceManager: deviceManager,
		logger:        logger,
	}

	devices := service.getAvailableDevices()
	if len(devices) != 0 {
		t.Errorf("Expected 0 devices, got %d", len(devices))
	}

	deviceManager.devices["test-device-1"] = &GPUDevice{
		ID:     "test-device-1",
		Health: HealthHealthy,
	}
	deviceManager.devices["test-device-2"] = &GPUDevice{
		ID:     "test-device-2",
		Health: HealthUnhealthy,
	}

	devices = service.getAvailableDevices()
	if len(devices) != 2 {
		t.Errorf("Expected 2 devices, got %d", len(devices))
	}

	var healthyCount, unhealthyCount int
	for _, device := range devices {
		if device.Health == pluginapi.Healthy {
			healthyCount++
		} else {
			unhealthyCount++
		}
	}

	if healthyCount != 1 {
		t.Errorf("Expected 1 healthy device, got %d", healthyCount)
	}
	if unhealthyCount != 1 {
		t.Errorf("Expected 1 unhealthy device, got %d", unhealthyCount)
	}
}

func TestDevicePluginService_prepareContainerDevices(t *testing.T) {
	logger, _ := test.NewNullLogger()

	deviceManager := &DeviceManager{
		devices: make(map[string]*GPUDevice),
		logger:  logger,
	}

	service := &DevicePluginService{
		deviceManager: deviceManager,
		resourceName:  "nvidia.com/gpu-h200",
		logger:        logger,
	}

	deviceManager.devices["test-device"] = &GPUDevice{
		ID:         "test-device",
		PCIAddress: "0000:00:01.0",
		VFIOGroup:  "1",
		Health:     HealthHealthy,
	}

	spec, err := service.prepareContainerDevices([]string{"test-device"})
	if err != nil {
		t.Errorf("prepareContainerDevices() error = %v", err)
		return
	}

	if len(spec.DeviceNodes) != 2 {
		t.Errorf("Expected 2 device nodes, got %d", len(spec.DeviceNodes))
	}

	expectedDeviceNodes := []string{"/dev/vfio/1", "/dev/vfio/vfio"}
	for _, expected := range expectedDeviceNodes {
		found := false
		for _, actual := range spec.DeviceNodes {
			if actual == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected device node %s not found", expected)
		}
	}

	expectedEnvs := map[string]string{
		"GPU_test-device_VFIO_GROUP":                  "1",
		"GPU_test-device_PCI_ADDRESS":                 "0000:00:01.0",
		"PCI_RESOURCE_NVIDIA_COM_NVIDIA_COM_GPU_H200": "0000:00:01.0",
	}

	for key, expectedValue := range expectedEnvs {
		if actualValue, exists := spec.Envs[key]; !exists || actualValue != expectedValue {
			t.Errorf("Expected env %s=%s, got %s=%s", key, expectedValue, key, actualValue)
		}
	}

	_, err = service.prepareContainerDevices([]string{"nonexistent-device"})
	if err == nil {
		t.Errorf("Expected error for nonexistent device")
	}

	deviceManager.devices["test-device-2"] = &GPUDevice{
		ID:         "test-device-2",
		PCIAddress: "0000:00:02.0",
		VFIOGroup:  "2",
		Health:     HealthHealthy,
	}

	specMultiple, err := service.prepareContainerDevices([]string{"test-device", "test-device-2"})
	if err != nil {
		t.Errorf("prepareContainerDevices() with multiple devices error = %v", err)
		return
	}

	expectedKubevirtEnv := "0000:00:01.0,0000:00:02.0"
	if actualValue, exists := specMultiple.Envs["PCI_RESOURCE_NVIDIA_COM_NVIDIA_COM_GPU_H200"]; !exists || actualValue != expectedKubevirtEnv {
		t.Errorf("Expected KubeVirt env to be %s, got %s", expectedKubevirtEnv, actualValue)
	}
}

func TestDeviceManager_UpdateDeviceHealth(t *testing.T) {
	logger, _ := test.NewNullLogger()

	dm := &DeviceManager{
		devices: make(map[string]*GPUDevice),
		logger:  logger,
	}

	device := &GPUDevice{
		ID:     "test-device",
		Health: HealthUnknown,
	}
	dm.devices["test-device"] = device

	err := dm.UpdateDeviceHealth("test-device", HealthHealthy)
	if err != nil {
		t.Errorf("UpdateDeviceHealth() error = %v", err)
	}

	if device.Health != HealthHealthy {
		t.Errorf("Expected device health to be HealthHealthy, got %v", device.Health)
	}

	err = dm.UpdateDeviceHealth("nonexistent", HealthHealthy)
	if err == nil {
		t.Errorf("Expected error for nonexistent device")
	}
}

func TestDeviceManager_GetDevice(t *testing.T) {
	logger, _ := test.NewNullLogger()

	dm := &DeviceManager{
		devices: make(map[string]*GPUDevice),
		logger:  logger,
	}

	testDevice := &GPUDevice{
		ID:         "test-device",
		PCIAddress: "0000:00:01.0",
		Health:     HealthHealthy,
	}
	dm.devices["test-device"] = testDevice

	device, exists := dm.GetDevice("test-device")
	if !exists {
		t.Errorf("Expected device to exist")
	}
	if device.ID != "test-device" {
		t.Errorf("Expected device ID test-device, got %s", device.ID)
	}

	_, exists = dm.GetDevice("nonexistent")
	if exists {
		t.Errorf("Expected device to not exist")
	}
}

func TestDevicePluginService_buildKubeVirtResourceKey(t *testing.T) {
	logger, _ := test.NewNullLogger()

	tests := []struct {
		name         string
		resourceName string
		expected     string
	}{
		{
			name:         "default resource name",
			resourceName: "nvidia.com/gpu-h200",
			expected:     "PCI_RESOURCE_NVIDIA_COM_NVIDIA_COM_GPU_H200",
		},
		{
			name:         "partitioned resource name",
			resourceName: "nvidia.com/gpu-h200-4x",
			expected:     "PCI_RESOURCE_NVIDIA_COM_NVIDIA_COM_GPU_H200_4X",
		},
		{
			name:         "empty resource name",
			resourceName: "",
			expected:     "",
		},
		{
			name:         "simple resource name",
			resourceName: "example/resource",
			expected:     "PCI_RESOURCE_NVIDIA_COM_EXAMPLE_RESOURCE",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &DevicePluginService{
				resourceName: tt.resourceName,
				logger:       logger,
			}

			result := service.buildKubeVirtResourceKey()
			if result != tt.expected {
				t.Errorf("buildKubeVirtResourceKey() = %v, expected %v", result, tt.expected)
			}
		})
	}
}
