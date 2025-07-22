package deviceplugin

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sirupsen/logrus/hooks/test"
)

func TestNewVFIOManager(t *testing.T) {
	logger, _ := test.NewNullLogger()

	vfio := NewVFIOManager(logger)

	if vfio.devicePath != PCIDevicesPath {
		t.Errorf("Expected device path %s, got %s", PCIDevicesPath, vfio.devicePath)
	}

	if vfio.groupPath != VFIOGroupPath {
		t.Errorf("Expected group path %s, got %s", VFIOGroupPath, vfio.groupPath)
	}

	if vfio.logger != logger {
		t.Errorf("Expected logger to be set correctly")
	}
}

func TestVFIOManager_readSysfsFile(t *testing.T) {
	logger, _ := test.NewNullLogger()
	vfio := NewVFIOManager(logger)

	tempDir := t.TempDir()

	testContent := "0x10de"
	testFile := filepath.Join(tempDir, "vendor")
	if err := os.WriteFile(testFile, []byte(testContent), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	content, err := vfio.readSysfsFile(tempDir, "vendor")
	if err != nil {
		t.Errorf("readSysfsFile() error = %v", err)
	}

	if content != testContent {
		t.Errorf("Expected content %s, got %s", testContent, content)
	}

	_, err = vfio.readSysfsFile(tempDir, "nonexistent")
	if err == nil {
		t.Errorf("Expected error for nonexistent file")
	}
}

func TestVFIOManager_readSysfsFile_driver(t *testing.T) {
	logger, _ := test.NewNullLogger()
	vfio := NewVFIOManager(logger)

	tempDir := t.TempDir()

	driverDir := filepath.Join(tempDir, "vfio-pci")
	if err := os.MkdirAll(driverDir, 0755); err != nil {
		t.Fatalf("Failed to create driver directory: %v", err)
	}

	driverSymlink := filepath.Join(tempDir, "driver")
	if err := os.Symlink(driverDir, driverSymlink); err != nil {
		t.Fatalf("Failed to create driver symlink: %v", err)
	}

	driver, err := vfio.readSysfsFile(tempDir, "driver")
	if err != nil {
		t.Errorf("readSysfsFile() error = %v", err)
	}

	if driver != "vfio-pci" {
		t.Errorf("Expected driver vfio-pci, got %s", driver)
	}
}

func createMockDevice(t *testing.T, basePath, pciAddress, vendorID, deviceID, driver string) {
	devicePath := filepath.Join(basePath, pciAddress)
	if err := os.MkdirAll(devicePath, 0755); err != nil {
		t.Fatalf("Failed to create device directory: %v", err)
	}

	vendorFile := filepath.Join(devicePath, "vendor")
	if err := os.WriteFile(vendorFile, []byte("0x"+vendorID), 0644); err != nil {
		t.Fatalf("Failed to write vendor file: %v", err)
	}

	deviceFile := filepath.Join(devicePath, "device")
	if err := os.WriteFile(deviceFile, []byte("0x"+deviceID), 0644); err != nil {
		t.Fatalf("Failed to write device file: %v", err)
	}

	enableFile := filepath.Join(devicePath, "enable")
	if err := os.WriteFile(enableFile, []byte("1"), 0644); err != nil {
		t.Fatalf("Failed to write enable file: %v", err)
	}

	configFile := filepath.Join(devicePath, "config")
	if err := os.WriteFile(configFile, []byte{}, 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	driverDir := filepath.Join(devicePath, driver)
	if err := os.MkdirAll(driverDir, 0755); err != nil {
		t.Fatalf("Failed to create driver directory: %v", err)
	}

	driverSymlink := filepath.Join(devicePath, "driver")
	if err := os.Symlink(driverDir, driverSymlink); err != nil {
		t.Fatalf("Failed to create driver symlink: %v", err)
	}

	iommuGroupDir := filepath.Join(basePath, "iommu_groups", "1")
	if err := os.MkdirAll(iommuGroupDir, 0755); err != nil {
		t.Fatalf("Failed to create IOMMU group directory: %v", err)
	}

	iommuGroupSymlink := filepath.Join(devicePath, "iommu_group")
	if err := os.Symlink(iommuGroupDir, iommuGroupSymlink); err != nil {
		t.Fatalf("Failed to create IOMMU group symlink: %v", err)
	}
}

func TestVFIOManager_DiscoverGPUs_MockFilesystem(t *testing.T) {
	logger, _ := test.NewNullLogger()

	tempDir := t.TempDir()

	vfio := &VFIOManager{
		devicePath: tempDir,
		groupPath:  filepath.Join(tempDir, "vfio"),
		logger:     logger,
	}

	createMockDevice(t, tempDir, "0000:01:00.0", H200VendorID, H200DeviceID, "vfio-pci")
	createMockDevice(t, tempDir, "0000:02:00.0", H200VendorID, "1234", "nvidia")       // Different device ID
	createMockDevice(t, tempDir, "0000:03:00.0", "8086", H200DeviceID, "vfio-pci")     // Different vendor
	createMockDevice(t, tempDir, "0000:04:00.0", H200VendorID, H200DeviceID, "nvidia") // Wrong driver

	vfioGroupPath := filepath.Join(tempDir, "vfio", "1")
	if err := os.MkdirAll(filepath.Dir(vfioGroupPath), 0755); err != nil {
		t.Fatalf("Failed to create VFIO group parent directory: %v", err)
	}
	if err := os.WriteFile(vfioGroupPath, []byte{}, 0644); err != nil {
		t.Fatalf("Failed to create VFIO group file: %v", err)
	}

	devices, err := vfio.DiscoverGPUs()
	if err != nil {
		t.Errorf("DiscoverGPUs() error = %v", err)
		return
	}

	if len(devices) != 1 {
		t.Errorf("Expected 1 device, got %d", len(devices))
		return
	}

	device := devices[0]
	if device.ID != "0000:01:00.0" {
		t.Errorf("Expected device ID 0000:01:00.0, got %s", device.ID)
	}

	if device.VendorID != H200VendorID {
		t.Errorf("Expected vendor ID %s, got %s", H200VendorID, device.VendorID)
	}

	if device.DeviceID != H200DeviceID {
		t.Errorf("Expected device ID %s, got %s", H200DeviceID, device.DeviceID)
	}

	if device.VFIOGroup != "1" {
		t.Errorf("Expected VFIO group 1, got %s", device.VFIOGroup)
	}

	if device.Health != HealthHealthy {
		t.Errorf("Expected device health HealthHealthy, got %v", device.Health)
	}
}

func TestVFIOManager_getVFIOGroup(t *testing.T) {
	logger, _ := test.NewNullLogger()

	tempDir := t.TempDir()

	vfio := &VFIOManager{
		devicePath: tempDir,
		logger:     logger,
	}

	devicePath := filepath.Join(tempDir, "0000:01:00.0")
	if err := os.MkdirAll(devicePath, 0755); err != nil {
		t.Fatalf("Failed to create device directory: %v", err)
	}

	iommuGroupDir := filepath.Join(tempDir, "iommu_groups", "5")
	if err := os.MkdirAll(iommuGroupDir, 0755); err != nil {
		t.Fatalf("Failed to create IOMMU group directory: %v", err)
	}

	iommuGroupSymlink := filepath.Join(devicePath, "iommu_group")
	if err := os.Symlink(iommuGroupDir, iommuGroupSymlink); err != nil {
		t.Fatalf("Failed to create IOMMU group symlink: %v", err)
	}

	group, err := vfio.getVFIOGroup("0000:01:00.0")
	if err != nil {
		t.Errorf("getVFIOGroup() error = %v", err)
	}

	if group != "5" {
		t.Errorf("Expected VFIO group 5, got %s", group)
	}

	_, err = vfio.getVFIOGroup("nonexistent")
	if err == nil {
		t.Errorf("Expected error for nonexistent device")
	}
}

func TestVFIOManager_getDeviceHealth(t *testing.T) {
	logger, _ := test.NewNullLogger()

	tempDir := t.TempDir()

	vfio := &VFIOManager{
		devicePath: tempDir,
		groupPath:  filepath.Join(tempDir, "vfio"),
		logger:     logger,
	}

	devicePath := filepath.Join(tempDir, "0000:01:00.0")
	if err := os.MkdirAll(devicePath, 0755); err != nil {
		t.Fatalf("Failed to create device directory: %v", err)
	}

	enableFile := filepath.Join(devicePath, "enable")
	if err := os.WriteFile(enableFile, []byte("1"), 0644); err != nil {
		t.Fatalf("Failed to write enable file: %v", err)
	}

	configFile := filepath.Join(devicePath, "config")
	if err := os.WriteFile(configFile, []byte{}, 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	iommuGroupDir := filepath.Join(tempDir, "iommu_groups", "1")
	if err := os.MkdirAll(iommuGroupDir, 0755); err != nil {
		t.Fatalf("Failed to create IOMMU group directory: %v", err)
	}

	iommuGroupSymlink := filepath.Join(devicePath, "iommu_group")
	if err := os.Symlink(iommuGroupDir, iommuGroupSymlink); err != nil {
		t.Fatalf("Failed to create IOMMU group symlink: %v", err)
	}

	vfioGroupPath := filepath.Join(tempDir, "vfio", "1")
	if err := os.MkdirAll(filepath.Dir(vfioGroupPath), 0755); err != nil {
		t.Fatalf("Failed to create VFIO group parent directory: %v", err)
	}
	if err := os.WriteFile(vfioGroupPath, []byte{}, 0644); err != nil {
		t.Fatalf("Failed to create VFIO group file: %v", err)
	}

	health, err := vfio.getDeviceHealth("0000:01:00.0")
	if err != nil {
		t.Errorf("getDeviceHealth() error = %v", err)
	}

	if health != HealthHealthy {
		t.Errorf("Expected HealthHealthy, got %v", health)
	}

	if err := os.WriteFile(enableFile, []byte("0"), 0644); err != nil {
		t.Fatalf("Failed to update enable file: %v", err)
	}

	health, err = vfio.getDeviceHealth("0000:01:00.0")
	if err != nil {
		t.Errorf("getDeviceHealth() error = %v", err)
	}

	if health != HealthUnhealthy {
		t.Errorf("Expected HealthUnhealthy, got %v", health)
	}
}

func TestVFIOManager_PrepareDeviceForContainer(t *testing.T) {
	logger, _ := test.NewNullLogger()

	tempDir := t.TempDir()

	vfio := &VFIOManager{
		devicePath: tempDir,
		groupPath:  filepath.Join(tempDir, "vfio"),
		logger:     logger,
	}

	devicePath := filepath.Join(tempDir, "0000:01:00.0")
	if err := os.MkdirAll(devicePath, 0755); err != nil {
		t.Fatalf("Failed to create device directory: %v", err)
	}

	iommuGroupDir := filepath.Join(tempDir, "iommu_groups", "1")
	if err := os.MkdirAll(iommuGroupDir, 0755); err != nil {
		t.Fatalf("Failed to create IOMMU group directory: %v", err)
	}

	iommuGroupSymlink := filepath.Join(devicePath, "iommu_group")
	if err := os.Symlink(iommuGroupDir, iommuGroupSymlink); err != nil {
		t.Fatalf("Failed to create IOMMU group symlink: %v", err)
	}

	vfioGroupPath := filepath.Join(tempDir, "vfio", "1")
	vfioControllerPath := filepath.Join(tempDir, "vfio", "vfio")
	if err := os.MkdirAll(filepath.Dir(vfioGroupPath), 0755); err != nil {
		t.Fatalf("Failed to create VFIO directory: %v", err)
	}
	if err := os.WriteFile(vfioGroupPath, []byte{}, 0644); err != nil {
		t.Fatalf("Failed to create VFIO group file: %v", err)
	}
	if err := os.WriteFile(vfioControllerPath, []byte{}, 0644); err != nil {
		t.Fatalf("Failed to create VFIO controller file: %v", err)
	}

	spec, err := vfio.PrepareDeviceForContainer("0000:01:00.0")
	if err != nil {
		t.Errorf("PrepareDeviceForContainer() error = %v", err)
		return
	}

	expectedDeviceNodes := []string{vfioGroupPath, vfioControllerPath}
	if len(spec.DeviceNodes) != len(expectedDeviceNodes) {
		t.Errorf("Expected %d device nodes, got %d", len(expectedDeviceNodes), len(spec.DeviceNodes))
	}

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
		"VFIO_GROUP":  "1",
		"PCI_ADDRESS": "0000:01:00.0",
		"VENDOR_ID":   H200VendorID,
		"DEVICE_ID":   H200DeviceID,
	}

	for key, expectedValue := range expectedEnvs {
		if actualValue, exists := spec.Envs[key]; !exists || actualValue != expectedValue {
			t.Errorf("Expected env %s=%s, got %s=%s", key, expectedValue, key, actualValue)
		}
	}

	if len(spec.Mounts) != 1 {
		t.Errorf("Expected 1 mount, got %d", len(spec.Mounts))
	}

	if spec.Mounts[0].HostPath != "/sys/bus/pci/devices/0000:01:00.0" {
		t.Errorf("Expected host path /sys/bus/pci/devices/0000:01:00.0, got %s", spec.Mounts[0].HostPath)
	}
}
