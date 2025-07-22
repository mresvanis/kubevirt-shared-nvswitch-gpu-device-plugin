package deviceplugin

import (
	"context"
	"fmt"
	"net"
	"os"
	"path"
	"reflect"
	"time"

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
	healthChecker *HealthChecker
	logger        *logrus.Logger
	ctx           context.Context
	cancel        context.CancelFunc
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

	healthChecker := &HealthChecker{
		deviceManager: deviceManager,
		checkInterval: DefaultHealthCheckInterval,
		logger:        config.Logger,
	}

	ctx, cancel := context.WithCancel(context.Background())

	service := &DevicePluginService{
		socketPath:    path.Join(config.SocketPath, DefaultSocketName),
		resourceName:  config.ResourceName,
		deviceManager: deviceManager,
		healthChecker: healthChecker,
		logger:        config.Logger,
		ctx:           ctx,
		cancel:        cancel,
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

	if err := s.healthChecker.Start(s.ctx); err != nil {
		return fmt.Errorf("failed to start health checker: %v", err)
	}

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

		spec.Envs[fmt.Sprintf("GPU_%s_VFIO_GROUP", deviceID)] = device.VFIOGroup
		spec.Envs[fmt.Sprintf("GPU_%s_PCI_ADDRESS", deviceID)] = device.PCIAddress

		spec.Annotations[fmt.Sprintf("gpu.nvidia.com/%s.vfio-group", deviceID)] = device.VFIOGroup
	}

	return spec, nil
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
