package deviceplugin

import (
	"context"
	"fmt"
	"net"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

const (
	DefaultSocketName = "shared-nvswitch-gpu.sock"

	KubeletSocket = "kubelet.sock"

	DevicePluginPath = "/var/lib/kubelet/device-plugins/"

	DefaultResourceName = "nvidia.com/gpu-h200"

	DefaultHealthCheckInterval = 30 * time.Second

	gpuPrefix = "PCI_RESOURCE_NVIDIA_COM"
)

type DevicePluginService struct {
	pluginapi.UnimplementedDevicePluginServer

	server        *grpc.Server
	socketPath    string
	resourceName  string
	deviceManager *DeviceManager
	logger        *logrus.Logger
	ctx           context.Context
	cancel        context.CancelFunc
	stop          chan struct{}
	healthy       chan string
	unhealthy     chan string
}

type DevicePlugin struct {
	service *DevicePluginService
	config  *Config
	logger  *logrus.Logger
}

func New(config *Config) (*DevicePlugin, error) {
	if config == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}

	if config.Logger == nil {
		return nil, fmt.Errorf("logger cannot be nil")
	}

	deviceManager := &DeviceManager{
		devices:    make(map[string]*GPUDevice),
		partitions: make(map[string]*GPUPartition),
		logger:     config.Logger,
	}

	ctx, cancel := context.WithCancel(context.Background())

	service := &DevicePluginService{
		socketPath:    path.Join(config.SocketPath, DefaultSocketName),
		resourceName:  config.ResourceName,
		deviceManager: deviceManager,
		logger:        config.Logger,
		ctx:           ctx,
		cancel:        cancel,
		stop:          make(chan struct{}),
		healthy:       make(chan string),
		unhealthy:     make(chan string),
	}

	return &DevicePlugin{
		service: service,
		config:  config,
		logger:  config.Logger,
	}, nil
}

func (dp *DevicePlugin) Start(ctx context.Context) error {
	dp.logger.Info("Starting device plugin")

	if err := dp.service.Start(); err != nil {
		return fmt.Errorf("failed to start device plugin service: %v", err)
	}

	if err := dp.registerWithKubelet(); err != nil {
		return fmt.Errorf("failed to register with kubelet: %v", err)
	}

	return nil
}

func (dp *DevicePlugin) Stop() {
	dp.logger.Info("Stopping device plugin")
	dp.service.Stop()
}

func (s *DevicePluginService) Start() error {
	s.logger.WithField("socket", s.socketPath).Info("Starting gRPC server")

	if err := s.cleanupSocket(); err != nil {
		return fmt.Errorf("failed to cleanup socket: %v", err)
	}

	l, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("failed to listen on socket %s: %v", s.socketPath, err)
	}

	s.server = grpc.NewServer()
	pluginapi.RegisterDevicePluginServer(s.server, s)

	if err := s.deviceManager.Start(); err != nil {
		return fmt.Errorf("failed to start device manager: %v", err)
	}

	go s.healthCheck()

	go func() {
		if err := s.server.Serve(l); err != nil {
			s.logger.WithError(err).Error("gRPC server failed")
		}
	}()

	s.logger.Info("Device plugin gRPC server started successfully")
	return nil
}

func (s *DevicePluginService) Stop() {
	s.cancel()
	close(s.stop)

	if s.server != nil {
		s.logger.Info("Stopping gRPC server")
		s.server.GracefulStop()
	}

	if err := s.cleanupSocket(); err != nil {
		s.logger.Errorf("Failed to cleanup socket: %v", err)
	}
}

func (s *DevicePluginService) cleanupSocket() error {
	if _, err := os.Stat(s.socketPath); err == nil {
		if err := os.Remove(s.socketPath); err != nil {
			return fmt.Errorf("failed to remove socket %s: %v", s.socketPath, err)
		}
	}
	return nil
}

func (s *DevicePluginService) ListAndWatch(e *pluginapi.Empty, srv pluginapi.DevicePlugin_ListAndWatchServer) error {
	s.logger.Info("ListAndWatch called")

	devices := s.getAvailableDevices()
	if err := srv.Send(&pluginapi.ListAndWatchResponse{Devices: devices}); err != nil {
		return fmt.Errorf("failed to send initial device list: %v", err)
	}

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			newDevices := s.getAvailableDevices()
			if !reflect.DeepEqual(devices, newDevices) {
				s.logger.WithField("device_count", len(newDevices)).Info("Device list changed, sending update")
				devices = newDevices
				if err := srv.Send(&pluginapi.ListAndWatchResponse{Devices: devices}); err != nil {
					return fmt.Errorf("failed to send device list update: %v", err)
				}
			}
		case <-s.ctx.Done():
			s.logger.Info("ListAndWatch context cancelled")
			return nil
		}
	}
}

func (s *DevicePluginService) Allocate(ctx context.Context, req *pluginapi.AllocateRequest) (*pluginapi.AllocateResponse, error) {
	s.logger.WithField("request", req).Info("Allocate called")

	responses := make([]*pluginapi.ContainerAllocateResponse, 0, len(req.ContainerRequests))

	for _, containerReq := range req.ContainerRequests {
		s.logger.WithField("devices", containerReq.DevicesIDs).Info("Allocating devices for container")

		deviceSpec, err := s.prepareContainerDevices(containerReq.DevicesIDs)
		if err != nil {
			return nil, fmt.Errorf("failed to prepare devices: %v", err)
		}

		response := &pluginapi.ContainerAllocateResponse{
			Envs:        deviceSpec.Envs,
			Annotations: deviceSpec.Annotations,
		}

		for _, mount := range deviceSpec.Mounts {
			response.Mounts = append(response.Mounts, &pluginapi.Mount{
				ContainerPath: mount.ContainerPath,
				HostPath:      mount.HostPath,
				ReadOnly:      false,
			})
		}

		for _, deviceNode := range deviceSpec.DeviceNodes {
			response.Devices = append(response.Devices, &pluginapi.DeviceSpec{
				ContainerPath: deviceNode,
				HostPath:      deviceNode,
				Permissions:   "rw",
			})
		}

		responses = append(responses, response)
	}

	return &pluginapi.AllocateResponse{ContainerResponses: responses}, nil
}

func (s *DevicePluginService) GetPreferredAllocation(ctx context.Context, req *pluginapi.PreferredAllocationRequest) (*pluginapi.PreferredAllocationResponse, error) {
	s.logger.WithField("request", req).Info("GetPreferredAllocation called")

	return &pluginapi.PreferredAllocationResponse{}, nil
}

func (s *DevicePluginService) PreStartContainer(ctx context.Context, req *pluginapi.PreStartContainerRequest) (*pluginapi.PreStartContainerResponse, error) {
	s.logger.WithField("request", req).Info("PreStartContainer called")

	return &pluginapi.PreStartContainerResponse{}, nil
}

func (s *DevicePluginService) GetDevicePluginOptions(ctx context.Context, e *pluginapi.Empty) (*pluginapi.DevicePluginOptions, error) {
	s.logger.Info("GetDevicePluginOptions called")

	return &pluginapi.DevicePluginOptions{
		PreStartRequired:                false,
		GetPreferredAllocationAvailable: true,
	}, nil
}

func (s *DevicePluginService) getAvailableDevices() []*pluginapi.Device {
	s.deviceManager.mutex.RLock()
	defer s.deviceManager.mutex.RUnlock()

	var devices []*pluginapi.Device
	for id, device := range s.deviceManager.devices {
		health := pluginapi.Healthy
		if device.Health != HealthHealthy {
			health = pluginapi.Unhealthy
		}

		devices = append(devices, &pluginapi.Device{
			ID:     id,
			Health: health,
		})
	}

	s.logger.WithField("device_count", len(devices)).Debug("Retrieved available devices")
	return devices
}

func (s *DevicePluginService) prepareContainerDevices(deviceIDs []string) (*ContainerDeviceSpec, error) {
	spec := &ContainerDeviceSpec{
		DeviceNodes: []string{},
		Mounts:      []Mount{},
		Envs:        make(map[string]string),
		Annotations: make(map[string]string),
	}

	var pciAddresses []string

	for _, deviceID := range deviceIDs {
		s.deviceManager.mutex.RLock()
		device, exists := s.deviceManager.devices[deviceID]
		s.deviceManager.mutex.RUnlock()

		if !exists {
			return nil, fmt.Errorf("device %s not found", deviceID)
		}

		vfioDevicePath := fmt.Sprintf("/dev/vfio/%s", device.VFIOGroup)
		spec.DeviceNodes = append(spec.DeviceNodes, vfioDevicePath)
		spec.DeviceNodes = append(spec.DeviceNodes, "/dev/vfio/vfio")

		pciAddresses = append(pciAddresses, device.PCIAddress)

		spec.Envs[fmt.Sprintf("GPU_%s_VFIO_GROUP", deviceID)] = device.VFIOGroup
		spec.Envs[fmt.Sprintf("GPU_%s_PCI_ADDRESS", deviceID)] = device.PCIAddress

		spec.Annotations[fmt.Sprintf("gpu.nvidia.com/%s.vfio-group", deviceID)] = device.VFIOGroup
	}

	kubevirtResourceKey := s.buildKubeVirtResourceKey()
	if kubevirtResourceKey != "" && len(pciAddresses) > 0 {
		spec.Envs[kubevirtResourceKey] = strings.Join(pciAddresses, ",")
	}

	return spec, nil
}

func (s *DevicePluginService) buildKubeVirtResourceKey() string {
	resourceName := s.resourceName

	if resourceName == "" {
		return ""
	}

	resourceName = strings.ToUpper(resourceName)
	resourceName = strings.ReplaceAll(resourceName, ".", "_")
	resourceName = strings.ReplaceAll(resourceName, "/", "_")
	resourceName = strings.ReplaceAll(resourceName, "-", "_")

	return fmt.Sprintf("%s_%s", gpuPrefix, resourceName)
}

func (dp *DevicePlugin) registerWithKubelet() error {
	dp.logger.Info("Registering device plugin with kubelet")

	kubeletEndpoint := path.Join(dp.config.SocketPath, KubeletSocket)
	conn, err := grpc.Dial("unix://"+kubeletEndpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("failed to dial kubelet: %v", err)
	}
	defer conn.Close()

	client := pluginapi.NewRegistrationClient(conn)
	request := &pluginapi.RegisterRequest{
		Version:      pluginapi.Version,
		Endpoint:     DefaultSocketName,
		ResourceName: dp.config.ResourceName,
	}

	_, err = client.Register(context.Background(), request)
	if err != nil {
		return fmt.Errorf("failed to register with kubelet: %v", err)
	}

	dp.logger.WithFields(logrus.Fields{
		"resource_name": dp.config.ResourceName,
		"endpoint":      DefaultSocketName,
	}).Info("Successfully registered with kubelet")

	return nil
}

func (s *DevicePluginService) healthCheck() error {
	method := "healthCheck"
	s.logger.WithField("method", method).Info("Starting health check")

	var pathDeviceMap = make(map[string]string)
	var devicePath = PCIDevicesPath

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		s.logger.WithField("method", method).WithError(err).Error("Unable to create fsnotify watcher")
		return err
	}
	defer watcher.Close()

	err = watcher.Add(filepath.Dir(s.socketPath))
	if err != nil {
		s.logger.WithField("method", method).WithError(err).Error("Unable to add device plugin socket path to fsnotify watcher")
		return err
	}

	_, err = os.Stat(devicePath)
	if err != nil {
		if !os.IsNotExist(err) {
			s.logger.WithField("method", method).WithError(err).Error("Unable to stat device path")
			return err
		}
	}

	s.deviceManager.mutex.RLock()
	for _, dev := range s.deviceManager.devices {
		deviceSysPath := filepath.Join(devicePath, dev.PCIAddress)
		err = watcher.Add(deviceSysPath)
		s.logger.WithFields(logrus.Fields{
			"method": method,
			"path":   deviceSysPath,
		}).Debug("Adding watcher to device path")
		pathDeviceMap[deviceSysPath] = dev.ID
		if err != nil {
			s.logger.WithField("method", method).WithError(err).WithField("device_path", deviceSysPath).Error("Unable to add device path to fsnotify watcher")
			return err
		}
	}
	s.deviceManager.mutex.RUnlock()

	for {
		select {
		case <-s.stop:
			s.logger.WithField("method", method).Info("Health check stopped")
			return nil
		case event := <-watcher.Events:
			deviceID, ok := pathDeviceMap[event.Name]
			if ok {
				if event.Op == fsnotify.Create {
					s.logger.WithFields(logrus.Fields{
						"method":    method,
						"device_id": deviceID,
						"event":     event.Op.String(),
						"path":      event.Name,
					}).Info("Device became healthy")
					s.deviceManager.UpdateDeviceHealth(deviceID, HealthHealthy)
					select {
					case s.healthy <- deviceID:
					default:
					}
				} else if (event.Op == fsnotify.Remove) || (event.Op == fsnotify.Rename) {
					s.logger.WithFields(logrus.Fields{
						"method":    method,
						"device_id": deviceID,
						"event":     event.Op.String(),
						"path":      event.Name,
					}).Warn("Marking device unhealthy")
					s.deviceManager.UpdateDeviceHealth(deviceID, HealthUnhealthy)
					select {
					case s.unhealthy <- deviceID:
					default:
					}
				}
			} else if event.Name == s.socketPath && event.Op == fsnotify.Remove {
				s.logger.WithField("method", method).Info("Socket path for GPU device was removed, kubelet likely restarted")
				return fmt.Errorf("socket removed, restart required")
			}
		case err := <-watcher.Errors:
			s.logger.WithField("method", method).WithError(err).Error("Watcher error")
		}
	}
}
