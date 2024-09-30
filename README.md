# CSI driver operators

This repository contains code for CSI driver operators that are part of
OpenShift payload and few optional ones that are installed by Operator
Lifecycle Manager (OLM).

## Operators

* aws-ebs-csi-driver-operator
* aws-efs-csi-driver-operator
* azure-disk-csi-driver-operator
* azure-file-csi-driver-operator
* smb-csi-driver-operator

## Automatic generation of CSI driver assets

As part of the repository, there is generator of CSI driver YAML files in
`cmd/generator`.

### Usage

`make update` will re-generate all assets automatically.

### Documentation

Some documentation is available via godoc. Usage:

```shell
godoc &
firefox localost:6060/pkg/github.com/openshift/csi-operator/
```

Good starting points are `pkg/generator` and `pkg/generated-assets`.

## Quick start

### AWS EBS CSI driver operator

Before running the operator manually, you must remove the operator installed by
CSO/CVO

```shell
# Scale down CVO and CSO
oc scale --replicas=0 deploy/cluster-version-operator -n openshift-cluster-version
oc scale --replicas=0 deploy/cluster-storage-operator -n openshift-cluster-storage-operator

# Delete operator resources (daemonset, deployments)
oc -n openshift-cluster-csi-drivers delete deployment.apps/aws-ebs-csi-driver-operator deployment.apps/aws-ebs-csi-driver-controller daemonset.apps/aws-ebs-csi-driver-node
```

To build and run the operator locally:

```shell
# Create only the resources the operator needs to run via CLI
oc apply -f https://raw.githubusercontent.com/openshift/cluster-storage-operator/master/assets/csidriveroperators/aws-ebs/standalone/generated/operator.openshift.io_v1_clustercsidriver_ebs.csi.aws.com.yaml

# Build the operator
make

# Set the environment variables
export DRIVER_IMAGE=quay.io/openshift/origin-aws-ebs-csi-driver:latest
export PROVISIONER_IMAGE=quay.io/openshift/origin-csi-external-provisioner:latest
export ATTACHER_IMAGE=quay.io/openshift/origin-csi-external-attacher:latest
export RESIZER_IMAGE=quay.io/openshift/origin-csi-external-resizer:latest
export SNAPSHOTTER_IMAGE=quay.io/openshift/origin-csi-external-snapshotter:latest
export NODE_DRIVER_REGISTRAR_IMAGE=quay.io/openshift/origin-csi-node-driver-registrar:latest
export LIVENESS_PROBE_IMAGE=quay.io/openshift/origin-csi-livenessprobe:latest
export KUBE_RBAC_PROXY_IMAGE=quay.io/openshift/origin-kube-rbac-proxy:latest

# Run the operator via CLI
./bin/aws-ebs-csi-driver-operator start --kubeconfig $MY_KUBECONFIG --namespace openshift-cluster-csi-drivers
```

## Migrating an existing operator

If you are looking to migrate an existing CSI Driver operator to the combined `csi-operator` operator, refer to [the docs](docs/migrating-operators.md)
