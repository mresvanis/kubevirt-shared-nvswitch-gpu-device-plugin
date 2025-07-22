# NVIDIA Shared NVSwitch GPU Device Plugin

A Kubernetes Device Plugin that enables dynamic allocation of NVIDIA GPU
partitions using shared NVSwitch multitenancy virtualization. This plugin
integrates with NVIDIA Fabric Manager Partition Manager (FMPM) to manage GPU
partitions of varying sizes and expose them to Kubernetes workloads through
VFIO-PCI passthrough.

## Overview

The Shared NVSwitch GPU Device Plugin provides:

- **Dynamic Partition Management**: Create and manage GPU partitions (1x, 2x, 4x, 8x, 16x) on NVIDIA NVSwitch capable systems (e.g. HGX H200)
- **VFIO-PCI Integration**: Secure GPU passthrough to containers using VFIO drivers
- **Fabric Manager Integration**: Seamless integration with NVIDIA Fabric Manager Partition Manager (FMPM)
- **Topology-Aware Allocation**: Intelligent partition placement considering NVSwitch fabric topology
- **Health Monitoring**: Continuous monitoring of GPU health and partition status
- **Kubernetes Native**: Standard Kubernetes Device Plugin API compliance

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                     Kubernetes Cluster                      │
├─────────────────────────────────────────────────────────────┤
│  ┌─────────────┐    ┌──────────────────────────────────┐    │
│  │   kubelet   │◄──►│   Shared NVSwitch Device Plugin  │    │
│  │             │    │  - ListAndWatch()                │    │
│  │             │    │  - Allocate()                    │    │
│  │             │    │  - GetPreferredAllocate()        │    │
│  └─────────────┘    └──────────────────────────────────┘    │
└─────────────────────────────────────────────────────────────┘
              │                             │
┌─────────────▼─────────────────────────────▼─────────────────┐
│                      Host System                            │
├─────────────────────────────────────────────────────────────┤
│  ┌──────────────────┐      ┌─────────────────────────────┐  │
│  │  VFIO-PCI Driver │      │   NVIDIA Driver + FMPM      │  │
│  │                  │◄────►│   + Fabric Manager          │  │
│  └──────────────────┘      └─────────────────────────────┘  │
├─────────────────────────────────────────────────────────────┤
│            NVIDIA HGX H200 Hardware                         │
│         8-16 × H200 GPUs + 2-4 × NVSwitch                   │
└─────────────────────────────────────────────────────────────┘
```

## Resource Types

The device plugin exposes multiple resource types for different partition sizes:

- `nvidia.com/gpu-h200-1x`: Single GPU partitions
- `nvidia.com/gpu-h200-2x`: Two GPU partitions
- `nvidia.com/gpu-h200-4x`: Four GPU partitions
- `nvidia.com/gpu-h200-8x`: Eight GPU partitions
- `nvidia.com/gpu-h200-16x`: Sixteen GPU partitions (dual-baseboard systems)

## Prerequisites

### Hardware Requirements
- NVIDIA HGX H200 system with NVSwitch
- IOMMU-capable CPU and motherboard
- At least 8 H200 GPUs for meaningful partitioning

### Software Requirements
- Linux kernel 4.15+ with VFIO support
- NVIDIA Driver
- NVIDIA Fabric Manager
- NVIDIA Fabric Manager Partition Manager (FMPM)
- Kubernetes 1.19+ with Device Plugin support
- Container runtime with device support (containerd/CRI-O)

### System Configuration

1. **Enable IOMMU in GRUB**:
   ```bash
   # Add to /etc/default/grub
   GRUB_CMDLINE_LINUX="intel_iommu=on iommu=pt"
   sudo update-grub
   sudo reboot
   ```

2. **Load required kernel modules**:
   ```bash
   modprobe vfio-pci
   modprobe vfio_iommu_type1
   ```

3. **Bind H200 GPUs to VFIO-PCI**:
   ```bash
   # NVIDIA H200 device ID
   echo "10de 2330" > /sys/bus/pci/drivers/vfio-pci/new_id
   ```

4. **Configure hugepages**:
   ```bash
   echo 2048 > /sys/kernel/mm/hugepages/hugepages-2048kB/nr_hugepages
   ```

## Installation

### Option 1: Deploy using Kustomize (Recommended)

```bash
# Deploy directly from GitHub repository
kubectl apply -k https://github.com/mresvanis/shared-nvswitch-gpu-device-plugin//deployments/containers

# Or deploy a specific version/branch
kubectl apply -k https://github.com/mresvanis/shared-nvswitch-gpu-device-plugin//deployments/containers?ref=v1.0.0

# Verify deployment
kubectl get daemonset -n nvidia-gpu-operator shared-nvswitch-device-plugin
kubectl get pods -n nvidia-gpu-operator -l name=shared-nvswitch-device-plugin
```

### Option 2: Deploy Individual Manifests

```bash
# Deploy directly from GitHub repository
kubectl apply -f https://raw.githubusercontent.com/mresvanis/shared-nvswitch-gpu-device-plugin/main/deployments/containers/rbac.yaml
kubectl apply -f https://raw.githubusercontent.com/mresvanis/shared-nvswitch-gpu-device-plugin/main/deployments/containers/configmap.yaml
kubectl apply -f https://raw.githubusercontent.com/mresvanis/shared-nvswitch-gpu-device-plugin/main/deployments/containers/service.yaml
kubectl apply -f https://raw.githubusercontent.com/mresvanis/shared-nvswitch-gpu-device-plugin/main/deployments/containers/daemonset.yaml

# Optional: Deploy ServiceMonitor for Prometheus
kubectl apply -f https://raw.githubusercontent.com/mresvanis/shared-nvswitch-gpu-device-plugin/main/deployments/containers/servicemonitor.yaml
```

### Option 3: Build from Source

```bash
# Clone the repository
git clone https://github.com/mresvanis/shared-nvswitch-gpu-device-plugin.git
cd shared-nvswitch-gpu-device-plugin

# Build the binary
make build

# Build container image
make image-build

# Deploy to Kubernetes
kubectl apply -k deployments/containers/
```

## Configuration

### Configuration Files

The deployment includes a comprehensive ConfigMap with all necessary configuration. The main configuration file is located at `deployments/containers/configmap.yaml` and includes:

- **Device Plugin Settings**: Socket path, resource prefix, health check intervals
- **FMPM Integration**: Connection endpoints, retry policies, timeouts
- **VFIO Configuration**: Device and group paths for GPU access
- **Partition Management**: Supported sizes, allocation strategies
- **Monitoring**: Metrics endpoint configuration
- **Logging**: Level and format settings

To customize the configuration:

```bash
# Edit the ConfigMap
kubectl edit configmap shared-nvswitch-device-plugin-config -n nvidia-gpu-operator

# Or update the file and reapply
kubectl apply -f deployments/containers/configmap.yaml
```

### Environment Variables

- `HGX_H200_CONFIG_PATH`: Path to configuration file
- `HGX_H200_LOG_LEVEL`: Override log level (debug, info, warn, error)
- `HGX_H200_FMPM_ENDPOINT`: Override FMPM endpoint
- `HGX_H200_SOCKET_PATH`: Override device plugin socket path

## Usage

### Basic Pod Example

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: gpu-workload
spec:
  containers:
  - name: cuda-app
    image: nvidia/cuda:12.0-runtime-ubuntu20.04
    resources:
      limits:
        nvidia.com/gpu-h200-4x: 1  # Request 4-GPU partition
    command: ["nvidia-smi"]
```

### Multi-GPU Workload

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: multi-gpu-training
spec:
  replicas: 2
  selector:
    matchLabels:
      app: training
  template:
    metadata:
      labels:
        app: training
    spec:
      containers:
      - name: pytorch-training
        image: pytorch/pytorch:latest
        resources:
          limits:
            nvidia.com/gpu-h200-8x: 1  # Request 8-GPU partition
        env:
        - name: CUDA_VISIBLE_DEVICES
          value: "all"
```

## Monitoring

### Prometheus Integration

The deployment includes comprehensive monitoring setup:

- **Service**: Headless service exposing metrics endpoint
- **ServiceMonitor**: Automatic Prometheus integration for metric collection
- **Built-in Health Probes**: Liveness and readiness probes

### Prometheus Metrics

The device plugin exposes metrics on port 9090:

- `hgx_h200_devices_total`: Total number of H200 devices by state
- `hgx_h200_partition_operations_total`: Partition operations counter
- `hgx_h200_allocation_duration_seconds`: Allocation latency histogram

### Health Endpoints

- `http://localhost:9090/healthz`: Liveness probe endpoint
- `http://localhost:9090/readyz`: Readiness probe endpoint
- `http://localhost:9090/metrics`: Prometheus metrics

### Viewing Metrics

```bash
# Port-forward to access metrics locally
kubectl port-forward -n nvidia-gpu-operator svc/shared-nvswitch-device-plugin-metrics 9090:9090

# View metrics
curl http://localhost:9090/metrics

# Check health
curl http://localhost:9090/healthz
```

## Troubleshooting

### Common Issues

1. **Device not found**:
   ```bash
   # Verify GPUs are bound to VFIO-PCI
   lspci -k | grep -A 3 H200
   ```

2. **FMPM connection failed**:
   ```bash
   # Check FMPM service status
   systemctl status nvidia-fabricmanager
   ```

3. **Permission denied**:
   ```bash
   # Verify VFIO permissions
   ls -la /dev/vfio/
   ```

### Debugging

Enable debug logging:
```bash
kubectl set env daemonset/shared-nvswitch-device-plugin -n nvidia-gpu-operator HGX_H200_LOG_LEVEL=debug
```

View device plugin logs:
```bash
kubectl logs -n nvidia-gpu-operator -l name=shared-nvswitch-device-plugin -f
```

Check deployment status:
```bash
# Check DaemonSet status
kubectl get daemonset -n nvidia-gpu-operator shared-nvswitch-device-plugin

# Check pod status on each node
kubectl get pods -n nvidia-gpu-operator -l name=shared-nvswitch-device-plugin -o wide

# Check node resources
kubectl describe node <node-name>
```

Verify GPU resources are available:
```bash
# Check if GPUs are exposed as resources
kubectl describe node <hgx-node> | grep nvidia.com/gpu-h200

# List all GPU resources in the cluster
kubectl get nodes -o json | jq '.items[].status.capacity | with_entries(select(.key | startswith("nvidia.com/gpu-h200")))'
```

## Development

### Building

```bash
# Install dependencies
make deps

# Format code
make fmt

# Run linter
make lint

# Run tests
make test

# Run tests with coverage
make test-cover

# Build binary
make build
```

### Testing

```bash
# Unit tests
go test -v ./pkg/...

# Integration tests (requires HGX H200 hardware)
go test -v ./tests/integration/...
```

## Contributing

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add some amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

## License

This project is licensed under the Apache License 2.0 - see the [LICENSE](LICENSE) file for details.

## Support

- **Documentation**: [Design Document](design-doc.md)
- **Issues**: [GitHub Issues](https://github.com/mresvanis/shared-nvswitch-gpu-device-plugin/issues)
- **Discussions**: [GitHub Discussions](https://github.com/mresvanis/shared-nvswitch-gpu-device-plugin/discussions)

## Acknowledgments

- NVIDIA for HGX H200 hardware and Fabric Manager
- Kubernetes Device Plugin community
- VFIO and PCI passthrough maintainers
