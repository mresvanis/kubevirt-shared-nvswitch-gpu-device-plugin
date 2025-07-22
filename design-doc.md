# NVIDIA HGX H200 Shared NVSwitch GPU Device Plugin - Design Document

## 1. Executive Summary

This document outlines the design and implementation plan for a Kubernetes
Device Plugin that manages NVIDIA GPUs in NVSwitch-capable systems (e.g. HGX H200)
using the shared NVSwitch multitenancy virtualization model. The device plugin
will integrate with NVIDIA Fabric Manager Partition Manager (FMPM) to dynamically
manage GPU partitions of varying sizes (1x, 2x, 4x, 8x, 16x) and expose them to
Kubernetes workloads through VFIO-PCI passthrough.

## 2. System Architecture

### 2.1 Architecture Overview

```
┌─────────────────────────────────────────────────────────────┐
│                     Kubernetes Cluster                      │
├─────────────────────────────────────────────────────────────┤
│  ┌─────────────┐    ┌──────────────────────────────────┐    │
│  │   kubelet   │◄──►│   Shared NVSwitch Device Plugin  │    │
│  │             │    │                                  │    │
│  │             │    │  ┌─────────────────────────────┐ │    │
│  │             │    │  │   Device Plugin Server      │ │    │
│  │             │    │  │  - ListAndWatch()           │ │    │
│  │             │    │  │  - Allocate()               │ │    │
│  │             │    │  │  - GetPreferredAllocate()   │ │    │
│  │             │    │  └─────────────────────────────┘ │    │
│  └─────────────┘    └──────────────────────────────────┘    │
└─────────────────────────────────────────────────────────────┘
              │                             │
              │                             │
┌─────────────▼─────────────────────────────▼─────────────────┐
│                      Host System                            │
├─────────────────────────────────────────────────────────────┤
│  ┌──────────────────┐      ┌─────────────────────────────┐  │
│  │  VFIO-PCI Driver │      │   NVIDIA Driver             │  │
│  │                  │      │   + Fabric Manager          │  │
│  │                  │      │   + FMPM                    │  │
│  │                  │      │                             │  │
│  │  ┌─────────────┐ │      │  ┌───────────────────────┐  │  │
│  │  │ GPU 0-15    │ │      │  │  NVSwitch Management  │  │  │
│  │  │ (H200)      │ │◄────►│  │  Partition Control    │  │  │
│  │  │             │ │      │  │  Fabric Coordination  │  │  │
│  │  └─────────────┘ │      │  └───────────────────────┘  │  │
│  └──────────────────┘      └─────────────────────────────┘  │
├─────────────────────────────────────────────────────────────┤
│            NVIDIA HGX H200 Hardware                         │
│  ┌─────────────────────────────────────────────────────┐    │
│  │  8-16 × H200 GPUs + 2-4 × NVSwitch                  │    │
│  └─────────────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────────────┘
```

### 2.2 Component Interactions

1. **Device Plugin ↔ kubelet**: Standard Kubernetes Device Plugin API (gRPC)
2. **Device Plugin ↔ VFIO-PCI**: Sysfs interface for GPU discovery and health
3. **Device Plugin ↔ FMPM**: Command-line interface or API for partition management
4. **FMPM ↔ Fabric Manager**: Internal communication for NVSwitch fabric coordination

## 3. Detailed Component Design

### 3.1 Device Plugin Server

The core Kubernetes Device Plugin implementation following the v1beta1 API specification.

#### 3.1.1 gRPC Service Interface

```go
type DevicePluginService struct {
    server         *grpc.Server
    socketPath     string
    resourceName   string
    deviceManager  *DeviceManager
    fmpmClient     *FMPMClient
    healthChecker  *HealthChecker
}

// Kubernetes Device Plugin API methods
func (s *DevicePluginService) ListAndWatch(e *pluginapi.Empty, srv pluginapi.DevicePlugin_ListAndWatchServer) error
func (s *DevicePluginService) Allocate(ctx context.Context, req *pluginapi.AllocateRequest) (*pluginapi.AllocateResponse, error)
func (s *DevicePluginService) GetPreferredAllocation(ctx context.Context, req *pluginapi.PreferredAllocationRequest) (*pluginapi.PreferredAllocationResponse, error)
func (s *DevicePluginService) PreStartContainer(ctx context.Context, req *pluginapi.PreStartContainerRequest) (*pluginapi.PreStartContainerResponse, error)
func (s *DevicePluginService) GetDevicePluginOptions(ctx context.Context, e *pluginapi.Empty) (*pluginapi.DevicePluginOptions, error)
```

#### 3.1.2 Resource Naming Convention

Each partition size will be exposed as a separate resource:
- `nvidia.com/gpu-h200-1x`: Single GPU partitions
- `nvidia.com/gpu-h200-2x`: Two GPU partitions
- `nvidia.com/gpu-h200-4x`: Four GPU partitions
- `nvidia.com/gpu-h200-8x`: Eight GPU partitions
- `nvidia.com/gpu-h200-16x`: Sixteen GPU partitions (dual-baseboard systems only)

### 3.2 Device Manager

Responsible for GPU discovery, tracking, and state management.

#### 3.2.1 Device Discovery

```go
type DeviceManager struct {
    devices        map[string]*GPUDevice
    partitions     map[string]*GPUPartition
    vfioManager    *VFIOManager
    healthMonitor  *HealthMonitor
    mutex          sync.RWMutex
}

type GPUDevice struct {
    ID             string
    PCIAddress     string
    VendorID       string
    DeviceID       string
    VFIOGroup      string
    Health         DeviceHealth
    LastSeen       time.Time
}

type GPUPartition struct {
    ID             string
    Size           int  // 1, 2, 4, 8, or 16
    GPUDevices     []string
    State          PartitionState
    AllocatedTo    string
    CreatedAt      time.Time
    ActivatedAt    *time.Time
}

type PartitionState int
const (
    PartitionAvailable PartitionState = iota
    PartitionActivating
    PartitionActive
    PartitionAllocated
    PartitionDeactivating
    PartitionFailed
)
```

#### 3.2.2 Device Discovery Process

1. **VFIO-PCI Enumeration**: Scan `/sys/bus/pci/devices/` for H200 GPUs bound to `vfio-pci`
2. **Health Validation**: Verify device accessibility and basic health status
3. **Partition Mapping**: Query FMPM for available partition configurations
4. **State Synchronization**: Align discovered devices with FMPM partition state

### 3.3 FMPM Integration Layer

Interface with NVIDIA Fabric Manager Partition Manager for dynamic partition control.

#### 3.3.1 FMPM Client

```go
type FMPMClient struct {
    endpoint       string
    connectionType FMPMConnectionType
    timeout        time.Duration
    retryPolicy    RetryPolicy
}

type FMPMConnectionType int
const (
    FMPMUnixSocket FMPMConnectionType = iota
    FMPMTCP
)

// FMPM Operations
func (c *FMPMClient) ListPartitions() ([]FMPMPartition, error)
func (c *FMPMClient) ActivatePartition(partitionID string) error
func (c *FMPMClient) DeactivatePartition(partitionID string) error
func (c *FMPMClient) GetPartitionStatus(partitionID string) (*FMPMPartitionStatus, error)
func (c *FMPMClient) QueryFailedDevices() ([]string, error)
```

#### 3.3.2 FMPM Data Structures

```go
type FMPMPartition struct {
    ID           string
    Size         int
    GPUIDs       []string
    State        string
    NVSwitchIDs  []string
    CreatedAt    time.Time
}

type FMPMPartitionStatus struct {
    ID           string
    State        string
    Health       string
    LastUpdated  time.Time
    ErrorMessage string
}
```

### 3.4 VFIO Manager

Handles VFIO-PCI driver interactions and GPU enumeration.

#### 3.4.1 VFIO Interface

```go
type VFIOManager struct {
    devicePath     string
    groupPath      string
    containerPath  string
}

func (v *VFIOManager) DiscoverGPUs() ([]GPUDevice, error)
func (v *VFIOManager) GetDeviceHealth(deviceID string) (DeviceHealth, error)
func (v *VFIOManager) GetVFIOGroup(pciAddress string) (string, error)
func (v *VFIOManager) PrepareDeviceForContainer(deviceID string) (*ContainerDeviceSpec, error)
```

#### 3.4.2 Container Integration

```go
type ContainerDeviceSpec struct {
    DeviceNodes    []string
    Mounts         []Mount
    Envs           map[string]string
    Annotations    map[string]string
}

type Mount struct {
    HostPath      string
    ContainerPath string
    Type          string
    Options       []string
}
```

### 3.5 Health Monitoring

Continuous monitoring of GPU and partition health status.

#### 3.5.1 Health Checker

```go
type HealthChecker struct {
    devices        *DeviceManager
    fmpmClient     *FMPMClient
    checkInterval  time.Duration
    healthHistory  map[string][]HealthCheck
}

type HealthCheck struct {
    DeviceID      string
    Timestamp     time.Time
    Status        HealthStatus
    Details       string
}

type HealthStatus int
const (
    HealthUnknown HealthStatus = iota
    HealthHealthy
    HealthUnhealthy
    HealthFailed
)
```

## 4. Kubernetes Integration

### 4.1 Device Plugin API Implementation

#### 4.1.1 ListAndWatch Implementation

```go
func (s *DevicePluginService) ListAndWatch(e *pluginapi.Empty, srv pluginapi.DevicePlugin_ListAndWatchServer) error {
    // Initial device list
    devices := s.getAvailableDevices()
    srv.Send(&pluginapi.ListAndWatchResponse{Devices: devices})
    
    // Watch for changes
    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()
    
    for {
        select {
        case <-ticker.C:
            newDevices := s.getAvailableDevices()
            if !reflect.DeepEqual(devices, newDevices) {
                devices = newDevices
                srv.Send(&pluginapi.ListAndWatchResponse{Devices: devices})
            }
        case <-s.ctx.Done():
            return nil
        }
    }
}
```

#### 4.1.2 GetPreferredAllocation Implementation

```go
func (s *DevicePluginService) GetPreferredAllocation(ctx context.Context, req *pluginapi.PreferredAllocationRequest) (*pluginapi.PreferredAllocationResponse, error) {
    // Parse requested partition size from resource name
    partitionSize := s.getPartitionSizeFromResource(req.ResourceName)
    
    // Query FMPM for optimal partition suggestions
    availablePartitions, err := s.fmpmClient.ListPartitions()
    if err != nil {
        return nil, err
    }
    
    // Apply allocation strategy
    preferred := s.selectOptimalPartition(availablePartitions, partitionSize, req.MustIncludeDeviceIDs)
    
    return &pluginapi.PreferredAllocationResponse{
        PreferredAllocation: preferred,
    }, nil
}
```

#### 4.1.3 Allocate Implementation

```go
func (s *DevicePluginService) Allocate(ctx context.Context, req *pluginapi.AllocateRequest) (*pluginapi.AllocateResponse, error) {
    responses := make([]*pluginapi.ContainerAllocateResponse, 0, len(req.ContainerRequests))
    
    for _, containerReq := range req.ContainerRequests {
        // Activate partition via FMPM
        partitionID := s.getPartitionIDFromDevices(containerReq.DevicesIDs)
        err := s.fmpmClient.ActivatePartition(partitionID)
        if err != nil {
            return nil, fmt.Errorf("failed to activate partition %s: %v", partitionID, err)
        }
        
        // Prepare container device specification
        deviceSpec, err := s.prepareContainerDevices(containerReq.DevicesIDs)
        if err != nil {
            return nil, fmt.Errorf("failed to prepare devices: %v", err)
        }
        
        responses = append(responses, &pluginapi.ContainerAllocateResponse{
            Envs:        deviceSpec.Envs,
            Mounts:      deviceSpec.Mounts,
            Devices:     deviceSpec.DeviceNodes,
            Annotations: deviceSpec.Annotations,
        })
    }
    
    return &pluginapi.AllocateResponse{ContainerResponses: responses}, nil
}
```

### 4.2 Resource Management Strategy

#### 4.2.1 Allocation Strategies

1. **First-Fit**: Allocate first available partition of requested size
2. **Best-Fit**: Allocate partition with minimal resource fragmentation
3. **Topology-Aware**: Prefer partitions on same NVSwitch fabric
4. **Load-Balanced**: Distribute allocations across available NVSwitches

#### 4.2.2 Partition Lifecycle Management

```
Available → Activating → Active → Allocated → Deactivating → Available
     ↓                                           ↑
   Failed ←─────────────────────────────────────┘
```

## 5. Configuration Management

### 5.1 Configuration Schema

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: shared-nvswitch-device-plugin-config
data:
  config.yaml: |
    device_plugin:
      socket_path: "/var/lib/kubelet/device-plugins/"
      resource_prefix: "nvidia.com"
      health_check_interval: "30s"
      
    fmpm:
      endpoint: "127.0.0.1:8080"
      connection_type: "tcp"  # tcp or unix_socket
      unix_socket_path: "/var/run/nvidia-fabricmanager/fmpm.sock"
      timeout: "30s"
      retry_policy:
        max_retries: 3
        backoff: "exponential"
        
    vfio:
      device_path: "/sys/bus/pci/devices"
      group_path: "/dev/vfio"
      
    partitions:
      supported_sizes: [1, 2, 4, 8, 16]
      allocation_strategy: "topology_aware"
      auto_deactivate_timeout: "300s"
      
    logging:
      level: "info"
      format: "json"
      
    monitoring:
      enabled: true
      port: 9090
      metrics_path: "/metrics"
```

### 5.2 Runtime Configuration

Environment variables for runtime configuration:
- `HGX_H200_CONFIG_PATH`: Path to configuration file
- `HGX_H200_LOG_LEVEL`: Override log level
- `HGX_H200_FMPM_ENDPOINT`: Override FMPM endpoint
- `HGX_H200_SOCKET_PATH`: Override device plugin socket path

## 6. Deployment Architecture

### 6.1 DaemonSet Specification

```yaml
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: shared-nvswitch-device-plugin
  namespace: kube-system
spec:
  selector:
    matchLabels:
      name: shared-nvswitch-device-plugin
  template:
    metadata:
      labels:
        name: shared-nvswitch-device-plugin
    spec:
      hostNetwork: true
      hostPID: true
      priorityClassName: system-node-critical
      nodeSelector:
        nvidia.com/hgx-h200: "true"
      tolerations:
      - key: nvidia.com/gpu
        operator: Exists
        effect: NoSchedule
      containers:
      - name: shared-nvswitch-device-plugin
        image: nvidia/shared-nvswitch-device-plugin:latest
        imagePullPolicy: Always
        securityContext:
          privileged: true
        env:
        - name: NODE_NAME
          valueFrom:
            fieldRef:
              fieldPath: spec.nodeName
        volumeMounts:
        - name: device-plugin
          mountPath: /var/lib/kubelet/device-plugins
        - name: dev
          mountPath: /dev
        - name: sys
          mountPath: /sys
        - name: proc
          mountPath: /proc
        - name: config
          mountPath: /etc/shared-nvswitch-device-plugin
        ports:
        - name: metrics
          containerPort: 9090
          protocol: TCP
      volumes:
      - name: device-plugin
        hostPath:
          path: /var/lib/kubelet/device-plugins
      - name: dev
        hostPath:
          path: /dev
      - name: sys
        hostPath:
          path: /sys
      - name: proc
        hostPath:
          path: /proc
      - name: config
        configMap:
          name: shared-nvswitch-device-plugin-config
```

### 6.2 Node Prerequisites

#### 6.2.1 Kernel Modules and Drivers

```bash
# Required kernel modules
modprobe vfio-pci
modprobe vfio_iommu_type1

# NVIDIA driver and Fabric Manager
# (Managed by NVIDIA GPU Operator or manual installation)
```

#### 6.2.2 System Configuration

```bash
# IOMMU enablement in GRUB
GRUB_CMDLINE_LINUX="intel_iommu=on iommu=pt"

# VFIO-PCI binding for H200 GPUs
echo "10de 2330" > /sys/bus/pci/drivers/vfio-pci/new_id

# Hugepage configuration for VFIO
echo 2048 > /sys/kernel/mm/hugepages/hugepages-2048kB/nr_hugepages
```

## 7. Error Handling and Recovery

### 7.1 Error Categories

1. **FMPM Communication Errors**: Network timeouts, authentication failures
2. **VFIO Driver Errors**: Device binding failures, permission issues
3. **Partition Management Errors**: Activation/deactivation failures
4. **Health Check Failures**: Device unavailability, fabric errors
5. **Resource Conflicts**: Double allocation, invalid requests

### 7.2 Recovery Strategies

#### 7.2.1 Automatic Recovery

```go
type RecoveryManager struct {
    maxRetries     int
    backoffPolicy  BackoffPolicy
    recoveryQueue  chan RecoveryTask
}

type RecoveryTask struct {
    Type        RecoveryType
    DeviceID    string
    PartitionID string
    Attempt     int
    LastError   error
}

func (r *RecoveryManager) HandlePartitionFailure(partitionID string, err error) {
    task := RecoveryTask{
        Type:        RecoveryPartitionReactivation,
        PartitionID: partitionID,
        Attempt:     1,
        LastError:   err,
    }
    r.recoveryQueue <- task
}
```

#### 7.2.2 Manual Intervention

- Administrative commands for partition reset
- Debug mode for detailed troubleshooting
- Emergency partition deactivation

## 8. Monitoring and Observability

### 8.1 Metrics

#### 8.1.1 Prometheus Metrics

```go
var (
    deviceTotal = prometheus.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "hgx_h200_devices_total",
            Help: "Total number of H200 devices",
        },
        []string{"node", "state"},
    )
    
    partitionOperations = prometheus.NewCounterVec(
        prometheus.CounterOpts{
            Name: "hgx_h200_partition_operations_total",
            Help: "Total partition operations",
        },
        []string{"operation", "size", "status"},
    )
    
    allocationDuration = prometheus.NewHistogramVec(
        prometheus.HistogramOpts{
            Name: "hgx_h200_allocation_duration_seconds",
            Help: "Time taken for partition allocation",
        },
        []string{"size"},
    )
)
```

#### 8.1.2 Key Metrics

- Device discovery and health status
- Partition activation/deactivation success rates
- Allocation latency and throughput
- FMPM communication metrics
- Resource utilization by partition size

### 8.2 Logging

#### 8.2.1 Structured Logging

```go
type Logger struct {
    *logrus.Logger
    fields logrus.Fields
}

func (l *Logger) LogDeviceEvent(event DeviceEvent) {
    l.WithFields(logrus.Fields{
        "event_type":  event.Type,
        "device_id":   event.DeviceID,
        "partition_id": event.PartitionID,
        "timestamp":   event.Timestamp,
    }).Info("Device event occurred")
}
```

#### 8.2.2 Log Categories

- Device discovery and health events
- Partition lifecycle operations
- FMPM communication logs
- Allocation and deallocation events
- Error conditions and recovery actions

### 8.3 Health Endpoints

```go
// Health check endpoint
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
    status := s.getOverallHealth()
    if status.Healthy {
        w.WriteHeader(http.StatusOK)
    } else {
        w.WriteHeader(http.StatusServiceUnavailable)
    }
    json.NewEncoder(w).Encode(status)
}

// Readiness probe
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
    if s.isReady() {
        w.WriteHeader(http.StatusOK)
        w.Write([]byte("OK"))
    } else {
        w.WriteHeader(http.StatusServiceUnavailable)
        w.Write([]byte("Not Ready"))
    }
}
```

## 9. Security Considerations

### 9.1 Privileged Operations

- Device plugin runs with privileged security context
- Access to `/dev/vfio/*` and `/sys/bus/pci/devices/*`
- Root permissions for VFIO group management

### 9.2 Container Security

- Minimal container image with only required dependencies
- Non-root user where possible
- Read-only root filesystem
- Resource limits and constraints

### 9.3 VFIO Security

- Proper IOMMU group isolation
- Device access control through VFIO groups
- Prevention of device sharing conflicts

## 10. Testing Strategy

### 10.1 Unit Testing

- Mock FMPM client for partition operations
- Mock VFIO manager for device discovery
- Device plugin API compliance tests
- Error handling and recovery scenarios

### 10.2 Integration Testing

- End-to-end allocation workflows
- FMPM integration testing
- Kubernetes pod deployment testing
- Multi-partition allocation scenarios

### 10.3 Performance Testing

- Allocation latency under load
- Concurrent allocation handling
- Large-scale partition management
- Resource cleanup performance

### 10.4 Failure Testing

- FMPM communication failures
- Device health degradation
- Network partition scenarios
- Node restart recovery

## 11. Implementation Roadmap

### Phase 1: Core Infrastructure (Weeks 1-4)
- [ ] Basic project structure and build system
- [ ] Device Plugin gRPC server implementation
- [ ] VFIO-PCI device discovery
- [ ] Basic health checking

### Phase 2: FMPM Integration (Weeks 5-8)
- [ ] FMPM client implementation
- [ ] Partition management logic
- [ ] Allocation workflow integration
- [ ] Error handling and recovery

### Phase 3: Advanced Features (Weeks 9-12)
- [ ] GetPreferredAllocation implementation
- [ ] Topology-aware allocation strategies
- [ ] Comprehensive monitoring and metrics
- [ ] Configuration management

### Phase 4: Testing and Hardening (Weeks 13-16)
- [ ] Comprehensive testing suite
- [ ] Performance optimization
- [ ] Documentation and deployment guides
- [ ] Production readiness assessment

## 12. Dependencies and Requirements

### 12.1 System Dependencies

- NVIDIA HGX H200 hardware
- NVIDIA Driver (compatible version)
- NVIDIA Fabric Manager
- VFIO-PCI kernel module
- Kubernetes 1.19+ with Device Plugin support

### 12.2 Software Dependencies

- Go 1.19+
- gRPC and Protocol Buffers
- Prometheus client library
- Kubernetes device plugin APIs
- Container runtime with device support

### 12.3 External Dependencies

- NVIDIA Fabric Manager Partition Manager (FMPM)
- Kubernetes kubelet
- Container runtime (containerd/CRI-O)

## 13. Documentation and Support

### 13.1 Deployment Documentation

- Installation and configuration guides
- Troubleshooting documentation
- Performance tuning guidelines
- Security best practices

### 13.2 API Documentation

- Device Plugin API reference
- Configuration schema documentation
- Monitoring and metrics guide
- Error codes and messages

### 13.3 Maintenance Documentation

- Upgrade procedures
- Backup and recovery processes
- Debugging and diagnostic tools
- Support escalation procedures

## Conclusion

This design document provides a comprehensive blueprint for implementing a specialized Kubernetes Device Plugin for NVIDIA HGX H200 systems using shared NVSwitch multitenancy. The plugin will bridge the gap between Kubernetes orchestration and NVIDIA's advanced GPU fabric management, enabling dynamic allocation of GPU partitions while maintaining optimal performance and resource utilization.

The modular architecture ensures maintainability and extensibility, while the comprehensive error handling and monitoring capabilities provide the reliability required for production deployment. The implementation roadmap provides a structured approach to developing and deploying this complex system in phases.
