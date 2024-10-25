package generator

import (
	"github.com/openshift/csi-operator/pkg/generator"
)

const (
	ProvisionerAssetName         = "common/sidecars/provisioner.yaml"
	AttacherAssetName            = "common/sidecars/attacher.yaml"
	SnapshotterAssetName         = "common/sidecars/snapshotter.yaml"
	ResizerAssetName             = "common/sidecars/resizer.yaml"
	LivenessProbeAssetName       = "common/sidecars/livenessprobe.yaml"
	NodeDriverRegistrarAssetName = "common/sidecars/node_driver_registrar.yaml"
)

var (
	// DefaultControllerAssets contains assets that most CSI drivers need to run in the control plane namespace.
	DefaultControllerAssets = generator.NewAssets(generator.AllFlavours,
		"base/cabundle_cm.yaml",
		"base/controller_sa.yaml",
		"base/controller_pdb.yaml",
	).WithAssets(generator.StandaloneOnly,
		// TODO: figure out metrics in hypershift - it's probably a different Prometheus there
		"base/rbac/kube_rbac_proxy_role.yaml",
		"base/rbac/kube_rbac_proxy_binding.yaml",
		"base/rbac/prometheus_role.yaml",
		"base/rbac/prometheus_binding.yaml",
	)
	// DefaultNodeAssets contains assets that most CSI drivers need to run in the guest cluster (or in standalone OCP).
	DefaultNodeAssets = generator.NewAssets(generator.AllFlavours,
		"base/node_sa.yaml",
		"base/rbac/privileged_role.yaml",
		"base/rbac/node_privileged_binding.yaml",
		// The controller Deployment runs leader election in the GUEST cluster
		"base/rbac/lease_leader_election_role.yaml",
		"base/rbac/lease_leader_election_binding.yaml",
	)

	// DefaultAssetPatches contains patches that most CSI drivers need applied to their control plane assets. It adds
	// standalone / hypershift specific fields to the generic assets.
	DefaultAssetPatches = generator.NewAssetPatches(generator.StandaloneOnly,
		"controller.yaml", "common/standalone/controller_add_affinity.yaml",
	).WithPatches(generator.HyperShiftOnly,
		"controller_sa.yaml", "common/hypershift/controller_sa_pull_secret.yaml",
		"controller.yaml", "common/hypershift/controller_add_affinity_tolerations.yaml",
		"controller.yaml", "common/hypershift/controller_add_kubeconfig_volume.yaml.patch",
	)
)

var (
	// DefaultProvisioner is definition of the default external-provisioner sidecar, together with this kube-rbac-proxy.
	DefaultProvisioner = generator.SidecarConfig{
		TemplateAssetName: ProvisionerAssetName,
		ExtraArguments:    nil,
		HasMetricsPort:    true,
		MetricPortName:    "provisioner-m",
		GuestAssetNames: []string{
			"base/rbac/main_provisioner_binding.yaml",
		},
		AssetPatches: generator.NewAssetPatches(generator.HyperShiftOnly,
			// Add the guest kubeconfig to the sidecar on HyperShift.
			"sidecar.yaml", "common/hypershift/sidecar_add_kubeconfig.yaml.patch",
		),
	}

	// DefaultProvisionerWithSnapshots is DefaultProvisioner with extra RBAC rules installed to support restoring
	// snapshots by the provisioner sidecar.
	DefaultProvisionerWithSnapshots = DefaultProvisioner.WithAdditionalAssets(
		"base/rbac/volumesnapshot_reader_provisioner_binding.yaml",
	)

	// DefaultAttacher is definition of the default external-attacher sidecar, together with this kube-rbac-proxy.
	DefaultAttacher = generator.SidecarConfig{
		TemplateAssetName: AttacherAssetName,
		ExtraArguments:    nil,
		HasMetricsPort:    true,
		MetricPortName:    "attacher-m",
		GuestAssetNames: []string{
			"base/rbac/main_attacher_binding.yaml",
		},
		AssetPatches: generator.NewAssetPatches(generator.HyperShiftOnly,
			// Add the guest kubeconfig to the sidecar on HyperShift.
			"sidecar.yaml", "common/hypershift/sidecar_add_kubeconfig.yaml.patch",
		),
	}
	// DefaultSnapshotter is definition of the default external-snasphotter sidecar, together with this kube-rbac-proxy.
	DefaultSnapshotter = generator.SidecarConfig{
		TemplateAssetName: SnapshotterAssetName,
		ExtraArguments:    nil,
		HasMetricsPort:    true,
		MetricPortName:    "snapshotter-m",
		GuestAssetNames: []string{
			"base/rbac/main_snapshotter_binding.yaml",
		},
		AssetPatches: generator.NewAssetPatches(generator.HyperShiftOnly,
			// Add the guest kubeconfig to the sidecar on HyperShift.
			"sidecar.yaml", "common/hypershift/sidecar_add_kubeconfig.yaml.patch",
		),
	}
	// DefaultResizer is definition of the default external-resizer sidecar, together with this kube-rbac-proxy.
	DefaultResizer = generator.SidecarConfig{
		TemplateAssetName: ResizerAssetName,
		ExtraArguments:    nil,
		HasMetricsPort:    true,
		MetricPortName:    "resizer-m",
		GuestAssetNames: []string{
			"base/rbac/main_resizer_binding.yaml",
			"base/rbac/storageclass_reader_resizer_binding.yaml",
		},
		AssetPatches: generator.NewAssetPatches(generator.HyperShiftOnly,
			// Add the guest kubeconfig to the sidecar on HyperShift.
			"sidecar.yaml", "common/hypershift/sidecar_add_kubeconfig.yaml.patch",
		),
	}
	// DefaultLivenessProbe is definition of the default livenessprobe sidecar.
	DefaultLivenessProbe = generator.SidecarConfig{
		TemplateAssetName: LivenessProbeAssetName,
		ExtraArguments:    nil,
		HasMetricsPort:    false,
	}

	// DefaultNodeDriverRegistrar is definition of the default node-driver-registrar sidecar.
	DefaultNodeDriverRegistrar = generator.SidecarConfig{
		TemplateAssetName: NodeDriverRegistrarAssetName,
		ExtraArguments:    nil,
		HasMetricsPort:    false,
	}
)
