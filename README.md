# Cinder CSI driver operator

An operator to deploy the [OpenStack Cinder CSI driver](https://github.com/openshift/cloud-provider-openstack/tree/master/pkg/csi/cinder) in OpenShift.

## Configuration

The operator can be configured using a config map, which by default is found at either `openshift-config / cinder-csi-config` or `openshift-config / cloud-provider-config`.
The former is preferred as it stores configuration solely for the Cinder CSI Driver.
It is supported starting in OpenShift 4.14 and should be used in all new deployments.
As the name might suggest, the latter stores configuration for both the Cinder CSI driver and the OpenStack Cloud Provider.
It is used for legacy reasons.

Three keys are supported:

<dl>
<dt>`config`</dt>
<dd>
This defines the main configuration for the Cinder CSI driver.
This configuration is validated and minimally modified by the operator.
</dd>
<dt>`ca-bundle.pem`</dt>
<dd>
A CA bundle.
If provided, this is extracted to `/etc/kubernetes/static-pod-resources/configmaps/cloud-config/ca-bundle.pem` in the pod.
</dd>
<dt>`enable_topology`</dt>
<dd>
Whether to enable topology support or not.
If undefined, the operator will configure this automatically.
</dd>
</dl>

For example, if using the `openshift-config / cinder-csi-config` config map:

```shell
oc get configmap -n openshift-config cinder-csi-config -o yaml
```

```yaml
apiVersion: v1
data:
  ca-bundle.pem: |
    <redacted>
  config: |
    [Global]
    ...
    [BlockStorage]
    ...
  enable_topology: "true"
kind: ConfigMap
metadata:
  name: cinder-csi-config
  namespace: openshift-config
```

Alternatively, if using the `openshift-config / cloud-provider-config` config map:

```shell
oc get configmap -n openshift-config cloud-provider-config -o yaml
```

```yaml
apiVersion: v1
data:
  config: |
    [Global]
    ...
    [BlockStorage]
    ...
kind: ConfigMap
metadata:
  name: cloud-provider-config
  namespace: openshift-config
```

> *Note*
> The `openshift-config / cloud-provider-config` config map stores configuration for both services for historical reasons: previously, block device management was handled by the cloud provider.
> This was decoupled in the 4.14 release, but support for loading configuration from the `openshift-config / cloud-provider-config` config map is retained to avoid breaking existing deployments.
> Only values from the `[Global]`, `[BlockStorage]` and `[Metadata]` sections are relevant. The remainder are ignored by the CSI driver.
> A full list of supported configuration options can be found in the [OpenStack Cloud Provider documentation](https://github.com/kubernetes/cloud-provider-openstack/blob/master/docs/cinder-csi-plugin/using-cinder-csi-plugin.md#driver-config).

> *Note*
> The name of the config map, `cloud-provider-config`, is configurable and is derived from the `infrastructure / cluster` CRD.
>
>     oc get infrastructure cluster -o=jsonpath="{.spec.cloudConfig.name}"
>     cloud-provider-config
>
> If this has been modified, then the Cinder CSI Driver Operator, Cluster Cloud Controller Manager Operator,
> and other operators and services that depend on this config map will use the modified name.
> This does not apply if using the newer `cinder-csi-config` config map.
> For more information, refer to the [OpenShift documentation](https://docs.openshift.com/container-platform/4.12/rest_api/config_apis/infrastructure-config-openshift-io-v1.html#spec-cloudconfig).

The configuration stored at `config` is modified, validated and saved to a new config map, stored at `openshift-cluster-csi-drivers / cloud-conf`, under the `cloud.conf` key.
This generated config map is what is ultimately used by the Cinder CSI Driver.
This allows the operator to automatically configure the Cinder CSI Driver and minimise the possibility of accidental misconfiguration.

```shell
apiVersion: v1
data:
  cloud.conf: |
    [Global]
    ...
    [BlockStorage]
    ...
  enable_topology: "true"
kind: ConfigMap
metadata:
  name: cloud-conf
  namespace: openshift-cluster-csi-drivers
```

Modifications to the generated `openshift-cluster-csi-drivers / cloud-conf` config map will be ignored and will be overridden by the operator.
Any changes made should be made to the `openshift-config / cinder-csi-config` or `openshift-config / cloud-provider-config` config maps.

## Development

Before running the operator manually, you must remove the operator installed by CVO and CSO:

```shell
# Scale down CVO and CSO
oc scale --replicas=0 deploy/cluster-version-operator -n openshift-cluster-version
oc scale --replicas=0 deploy/cluster-storage-operator -n openshift-cluster-storage-operator

# Delete operator resources (daemonset, deployments)
oc -n openshift-cluster-csi-drivers delete deployment.apps/openstack-cinder-csi-driver-operator deployment.apps/openstack-cinder-csi-driver-controller daemonset.apps/openstack-cinder-csi-driver-node
```

You can then build and run the operator locally:

```shell
# Create only the resources the operator needs to run via CLI
oc apply -f https://raw.githubusercontent.com/openshift/cluster-storage-operator/master/assets/csidriveroperators/openstack-cinder/08_cr.yaml

# Build the operator
make

# Set the environment variables
export DRIVER_IMAGE=quay.io/openshift/origin-openstack-cinder-csi-driver:latest
export PROVISIONER_IMAGE=quay.io/openshift/origin-csi-external-provisioner:latest
export ATTACHER_IMAGE=quay.io/openshift/origin-csi-external-attacher:latest
export RESIZER_IMAGE=quay.io/openshift/origin-csi-external-resizer:latest
export SNAPSHOTTER_IMAGE=quay.io/openshift/origin-csi-external-snapshotter:latest
export NODE_DRIVER_REGISTRAR_IMAGE=quay.io/openshift/origin-csi-node-driver-registrar:latest
export LIVENESS_PROBE_IMAGE=quay.io/openshift/origin-csi-livenessprobe:latest
export KUBE_RBAC_PROXY_IMAGE=quay.io/openshift/origin-kube-rbac-proxy:latest

# Run the operator via CLI
./openstack-cinder-csi-driver-operator start --kubeconfig $MY_KUBECONFIG --namespace openshift-cluster-csi-drivers
```
