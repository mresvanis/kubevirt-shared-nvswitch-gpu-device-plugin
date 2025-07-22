package deviceplugin

import (
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

type Config struct {
	SocketPath   string
	ResourceName string
	ConfigPath   string
	Logger       *logrus.Logger
}

type DeviceHealth int

const (
	HealthUnknown DeviceHealth = iota
	HealthHealthy
	HealthUnhealthy
	HealthFailed
)

type PartitionState int

const (
	PartitionAvailable PartitionState = iota
	PartitionActivating
	PartitionActive
	PartitionAllocated
	PartitionDeactivating
	PartitionFailed
)

type GPUDevice struct {
	ID         string
	PCIAddress string
	VendorID   string
	DeviceID   string
	VFIOGroup  string
	Health     DeviceHealth
	LastSeen   time.Time
}

type GPUPartition struct {
	ID          string
	Size        int
	GPUDevices  []string
	State       PartitionState
	AllocatedTo string
	CreatedAt   time.Time
	ActivatedAt *time.Time
}

type DeviceManager struct {
	devices    map[string]*GPUDevice
	partitions map[string]*GPUPartition
	mutex      sync.RWMutex
	logger     *logrus.Logger
}

type HealthStatus int

const (
	HealthStatusUnknown HealthStatus = iota
	HealthStatusHealthy
	HealthStatusUnhealthy
	HealthStatusFailed
)

type HealthCheck struct {
	DeviceID  string
	Timestamp time.Time
	Status    HealthStatus
	Details   string
}

type ContainerDeviceSpec struct {
	DeviceNodes []string
	Mounts      []Mount
	Envs        map[string]string
	Annotations map[string]string
}

type Mount struct {
	HostPath      string
	ContainerPath string
	Type          string
	Options       []string
}
