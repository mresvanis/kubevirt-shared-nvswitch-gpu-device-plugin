package deviceplugin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

const (
	PCIDevicesPath    = "/sys/bus/pci/devices"
	VFIOGroupPath     = "/dev/vfio"
	H200VendorID      = "10de"
	H200DeviceID      = "2335"
	VFIOPCIDriverName = "vfio-pci"
)

type VFIOManager struct {
	devicePath string
	groupPath  string
	logger     *logrus.Logger
}

func NewVFIOManager(logger *logrus.Logger) *VFIOManager {
	return &VFIOManager{
		devicePath: PCIDevicesPath,
		groupPath:  VFIOGroupPath,
		logger:     logger,
	}
}

func (v *VFIOManager) DiscoverGPUs() ([]GPUDevice, error) {
	v.logger.Info("Starting GPU discovery")
	v.logger.WithFields(logrus.Fields{
		"device_path":     v.devicePath,
		"expected_vendor": H200VendorID,
		"expected_device": H200DeviceID,
		"expected_driver": VFIOPCIDriverName,
	}).Info("Discovery parameters")

	var devices []GPUDevice

	entries, err := os.ReadDir(v.devicePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read PCI devices directory: %v", err)
	}

	v.logger.WithField("total_pci_devices", len(entries)).Info("Found PCI devices to scan")

	processedCount := 0
	nvidiaDeviceCount := 0

	for _, entry := range entries {
		// PCI devices can be directories or symlinks
		if !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
			continue
		}

		processedCount++

		pciAddress := entry.Name()
		devicePath := filepath.Join(v.devicePath, pciAddress)

		vendorID, err := v.readSysfsFile(devicePath, "vendor")
		if err != nil {
			v.logger.WithFields(logrus.Fields{
				"pci_address": pciAddress,
				"error":       err,
			}).Info("Failed to read vendor ID")
			continue
		}

		vendorID = strings.TrimSpace(strings.TrimPrefix(vendorID, "0x"))

		if vendorID == H200VendorID {
			nvidiaDeviceCount++
			v.logger.WithFields(logrus.Fields{
				"pci_address": pciAddress,
				"vendor_id":   vendorID,
			}).Info("Found NVIDIA device, checking device ID")
		}

		if vendorID != H200VendorID {
			continue
		}

		deviceID, err := v.readSysfsFile(devicePath, "device")
		if err != nil {
			v.logger.WithFields(logrus.Fields{
				"pci_address": pciAddress,
				"error":       err,
			}).Debug("Failed to read device ID")
			continue
		}

		deviceID = strings.TrimSpace(strings.TrimPrefix(deviceID, "0x"))

		if deviceID != H200DeviceID {
			v.logger.WithFields(logrus.Fields{
				"pci_address": pciAddress,
				"device_id":   deviceID,
				"expected":    H200DeviceID,
			}).Info("Device ID mismatch, skipping device")
			continue
		}

		v.logger.WithFields(logrus.Fields{
			"pci_address": pciAddress,
			"vendor_id":   vendorID,
			"device_id":   deviceID,
		}).Info("Found matching H200 GPU, checking driver binding")

		driver, err := v.readSysfsFile(devicePath, "driver")
		if err != nil {
			v.logger.WithFields(logrus.Fields{
				"pci_address": pciAddress,
				"error":       err,
			}).Debug("Failed to read driver info")
			continue
		}

		if !strings.Contains(driver, VFIOPCIDriverName) {
			v.logger.WithFields(logrus.Fields{
				"pci_address":     pciAddress,
				"driver":          driver,
				"expected_driver": VFIOPCIDriverName,
			}).Warn("Device not bound to vfio-pci driver, skipping")
			continue
		}

		v.logger.WithFields(logrus.Fields{
			"pci_address": pciAddress,
			"driver":      driver,
		}).Info("Device is bound to vfio-pci, checking VFIO group")

		vfioGroup, err := v.getVFIOGroup(pciAddress)
		if err != nil {
			v.logger.WithFields(logrus.Fields{
				"pci_address": pciAddress,
				"error":       err,
			}).Warn("Failed to get VFIO group")
			continue
		}

		health := HealthHealthy

		device := GPUDevice{
			ID:         pciAddress,
			PCIAddress: pciAddress,
			VendorID:   vendorID,
			DeviceID:   deviceID,
			VFIOGroup:  vfioGroup,
			Health:     health,
			LastSeen:   time.Now(),
		}

		devices = append(devices, device)

		v.logger.WithFields(logrus.Fields{
			"pci_address": pciAddress,
			"vfio_group":  vfioGroup,
			"health":      health,
		}).Info("Discovered H200 GPU")
	}

	v.logger.WithFields(logrus.Fields{
		"processed_devices": processedCount,
		"nvidia_devices":    nvidiaDeviceCount,
		"h200_devices":      len(devices),
	}).Info("GPU discovery completed")
	return devices, nil
}

func (v *VFIOManager) getVFIOGroup(pciAddress string) (string, error) {
	iommuGroupPath := filepath.Join(v.devicePath, pciAddress, "iommu_group")

	target, err := os.Readlink(iommuGroupPath)
	if err != nil {
		return "", fmt.Errorf("failed to read IOMMU group symlink: %v", err)
	}

	groupName := filepath.Base(target)
	if groupName == "" || groupName == "." {
		return "", fmt.Errorf("invalid IOMMU group name")
	}

	return groupName, nil
}

func (v *VFIOManager) GetVFIOGroup(pciAddress string) (string, error) {
	return v.getVFIOGroup(pciAddress)
}

func (v *VFIOManager) PrepareDeviceForContainer(deviceID string) (*ContainerDeviceSpec, error) {
	vfioGroup, err := v.getVFIOGroup(deviceID)
	if err != nil {
		return nil, fmt.Errorf("failed to get VFIO group for device %s: %v", deviceID, err)
	}

	vfioDevicePath := filepath.Join(v.groupPath, vfioGroup)
	vfioControllerPath := filepath.Join(v.groupPath, "vfio")

	if _, err := os.Stat(vfioDevicePath); err != nil {
		return nil, fmt.Errorf("VFIO device %s not accessible: %v", vfioDevicePath, err)
	}

	if _, err := os.Stat(vfioControllerPath); err != nil {
		return nil, fmt.Errorf("VFIO controller %s not accessible: %v", vfioControllerPath, err)
	}

	spec := &ContainerDeviceSpec{
		DeviceNodes: []string{
			vfioDevicePath,
			vfioControllerPath,
		},
		Mounts: []Mount{
			{
				HostPath:      "/sys/bus/pci/devices/" + deviceID,
				ContainerPath: "/sys/bus/pci/devices/" + deviceID,
				Type:          "bind",
				Options:       []string{"ro"},
			},
		},
		Envs: map[string]string{
			"VFIO_GROUP":  vfioGroup,
			"PCI_ADDRESS": deviceID,
			"VENDOR_ID":   H200VendorID,
			"DEVICE_ID":   H200DeviceID,
		},
		Annotations: map[string]string{
			"gpu.nvidia.com/vfio-group":  vfioGroup,
			"gpu.nvidia.com/pci-address": deviceID,
			"gpu.nvidia.com/vendor-id":   H200VendorID,
			"gpu.nvidia.com/device-id":   H200DeviceID,
		},
	}

	return spec, nil
}

func (v *VFIOManager) readSysfsFile(devicePath, fileName string) (string, error) {
	filePath := filepath.Join(devicePath, fileName)

	if fileName == "driver" {
		target, err := os.Readlink(filePath)
		if err != nil {
			return "", fmt.Errorf("failed to read driver symlink: %v", err)
		}
		return filepath.Base(target), nil
	}

	content, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to read file %s: %v", filePath, err)
	}

	return string(content), nil
}

func (dm *DeviceManager) Start() error {
	dm.logger.Info("Starting device manager")

	vfioManager := NewVFIOManager(dm.logger)

	devices, err := vfioManager.DiscoverGPUs()
	if err != nil {
		return fmt.Errorf("failed to discover GPUs: %v", err)
	}

	dm.mutex.Lock()
	defer dm.mutex.Unlock()

	for _, dev := range devices {
		dm.devices[dev.ID] = &dev
		dm.logger.WithFields(logrus.Fields{
			"device_id":   dev.ID,
			"pci_address": dev.PCIAddress,
			"vfio_group":  dev.VFIOGroup,
		}).Info("Registered GPU device")
	}

	dm.logger.WithField("device_count", len(dm.devices)).Info("Device manager started successfully")
	return nil
}

func (dm *DeviceManager) GetDevice(deviceID string) (*GPUDevice, bool) {
	dm.mutex.RLock()
	defer dm.mutex.RUnlock()

	device, exists := dm.devices[deviceID]
	return device, exists
}

func (dm *DeviceManager) UpdateDeviceHealth(deviceID string, health DeviceHealth) error {
	dm.mutex.Lock()
	defer dm.mutex.Unlock()

	device, exists := dm.devices[deviceID]
	if !exists {
		return fmt.Errorf("device %s not found", deviceID)
	}

	device.Health = health
	device.LastSeen = time.Now()

	dm.logger.WithFields(logrus.Fields{
		"device_id": deviceID,
		"health":    health,
	}).Debug("Updated device health")

	return nil
}

func (dm *DeviceManager) RefreshDevices() error {
	dm.logger.Info("Refreshing device list")

	vfioManager := NewVFIOManager(dm.logger)

	devices, err := vfioManager.DiscoverGPUs()
	if err != nil {
		return fmt.Errorf("failed to discover GPUs during refresh: %v", err)
	}

	dm.mutex.Lock()
	defer dm.mutex.Unlock()

	newDevices := make(map[string]*GPUDevice)
	for _, dev := range devices {
		newDevices[dev.ID] = &dev
	}

	for id := range dm.devices {
		if _, exists := newDevices[id]; !exists {
			dm.logger.WithField("device_id", id).Warn("Device no longer available")
		}
	}

	for id := range newDevices {
		if _, exists := dm.devices[id]; !exists {
			dm.logger.WithField("device_id", id).Info("New device discovered")
		}
	}

	dm.devices = newDevices
	dm.logger.WithField("device_count", len(dm.devices)).Info("Device refresh completed")

	return nil
}
