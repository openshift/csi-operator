This file provides guidance to AI agents when working with code in this repository.

This is the OpenShift CSI Operator monorepo - a consolidated repository containing multiple CSI (Container Storage Interface) driver operators for OpenShift. It contains:

- Multiple CSI driver operators (AWS EBS/EFS, Azure Disk/File, OpenStack Cinder/Manila, Samba)
- Asset generation system for Kubernetes manifests
- Shared operator controller infrastructure
- Support for both Standalone and HyperShift cluster deployments

## Key Architecture Components

### Asset Generation System

The asset generation system (`pkg/generator/`, `cmd/generator/`) creates Kubernetes YAML manifests from templates:

- Base templates (`assets/base/`) - Common resources shared across all drivers
- Driver-specific templates (`assets/overlays/<driver>/`) - Driver-specific configurations
- Generator processes templates and produces manifests in `assets/overlays/<driver>/generated/` (embedded in binaries via `assets/assets.go`)

**ClusterFlavour** determines deployment model:
- `FlavourStandalone` - Regular OpenShift clusters
- `FlavourHyperShift` - Hosted control plane clusters

**Template Variables**:
- `${ASSET_PREFIX}` - Full driver name (e.g., "aws-ebs-csi-driver")
- `${ASSET_SHORT_PREFIX}` - Short name (e.g., "ebs")
- `${DRIVER_IMAGE}`, `${PROVISIONER_IMAGE}`, etc. - Container images

**Patching**:
- Strategic merge patches - Preferred method for extending assets
- JSON patches - Surgical modifications (file suffix: `.patch`)

### Operator Structure

Each CSI driver operator consists of:

- `cmd/<driver>-csi-driver-operator/` - Main entry point with cobra command setup
- `pkg/driver/<driver>/` - Driver configuration and hooks
- `pkg/operator/` - Shared controller infrastructure
- `assets/overlays/<driver>/` - Driver-specific YAML templates and patches

**Controllers**:
- Deployment controller - Manages CSI controller pods
- DaemonSet controller - Manages CSI node pods
- StorageClass controller
- VolumeSnapshotClass controller
- StaticResources controller - RBAC, Services, ConfigMaps

**Hooks** provide driver-specific customization:
- `DeploymentHooks` - Modify controller Deployment (inject credentials, configure containers)
- `DaemonSetHooks` - Modify node DaemonSet
- `StorageClassHooks` - Customize StorageClass parameters
- `CredentialsRequestHooks` - Handle cloud credential requests

### CSI Sidecars

Standard Kubernetes CSI sidecars used:
- csi-provisioner - Creates/deletes volumes
- csi-attacher - Attaches volumes to nodes
- csi-resizer - Expands volumes
- csi-snapshotter - Creates volume snapshots
- csi-node-driver-registrar - Registers driver with kubelet
- livenessprobe - Health checking
- kube-rbac-proxy - Metrics authentication/authorization

## Common Development Commands

### Building

```bash
make build              # Build all operator binaries
make clean              # Clean build artifacts
```

### Asset Generation

```bash
make update             # Regenerate all assets and update metadata
```

This runs the generator which processes driver configurations from `pkg/driver/*/` and templates from `assets/` to generate final manifests in `assets/overlays/<driver>/generated`.

**Important:** Always run `make update` after modifying asset templates, driver configurations, or sidecar definitions.

### Verification

```bash
make verify             # Verify generated assets are up to date
git diff --exit-code    # Check for uncommitted changes
```

### Metadata Updates

```bash
make metadata OCP_VERSION=4.20   # Update OLM metadata for OCP version
```

Updates package versions in `config/aws-efs/` and `config/samba/` manifests.


## Important Conventions

### Port Allocation

Ports are registered in `pkg/driver/common/generator/port_registry.go`:
- Loopback metrics (container ports): 8200-8299 range
- Exposed metrics (service/host ports): 9200-9299 range

Always check the port registry before assigning new ports on nodes, i.e. when a Pod uses `hostNetwork: true`.

### Namespaces

- Control plane namespace:
  - Standalone: `openshift-cluster-csi-drivers`
  - HyperShift: `clusters-<guest-cluster-name>` or similar
- Guest namespace: Always `openshift-cluster-csi-drivers`

### Resource Naming

- Deployment: `${ASSET_PREFIX}-controller`
- DaemonSet: `${ASSET_PREFIX}-node`
- ServiceAccount: `${ASSET_PREFIX}-controller-sa`, `${ASSET_PREFIX}-node-sa`
- Service: `${ASSET_PREFIX}-controller-metrics`

### Code Organization

- Driver-specific code: `pkg/driver/<driver>/`
- Shared operator logic: `pkg/operator/`
- Common utilities: `pkg/driver/common/`
- Client management: `pkg/clients/`
- Asset handling: `pkg/generated-assets/`, `pkg/generator/`

## Common Development Notes

### Asset Generation
- Generated files are checked into git and must be committed
- Assets are embedded into binaries using go:embed
- Always run `make update` after template changes

### HyperShift Support
- Not all drivers support HyperShift
- Check `StandaloneOnly` flag in driver configuration
- HyperShift operators require `--guest-kubeconfig` parameter

### Cloud Credentials
- Cloud drivers watch credential secrets via hooks
- Credential changes trigger automatic pod restarts
- Credentials injected through DeploymentHooks/DaemonSetHooks

### Metrics
- All sidecars expose metrics on dedicated ports
- kube-rbac-proxy protects metrics endpoints
- ServiceMonitor automatically generated for Prometheus

### Images
- Image URLs provided via environment variables at runtime
- Use library-go constants for standard CSI sidecars
- Driver images are driver-specific
