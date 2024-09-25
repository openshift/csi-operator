# Manila CSI driver operator

An operator to deploy the [Manila CSI driver](https://github.com/openshift/cloud-provider-openstack/tree/master/pkg/csi/manila) in OpenShift.

## Design

The operator is based on [openshift/library-go](https://github.com/openshift/library-go). It manages `ClusterCSIDriver` instance named `manila.csi.openstack.org` and runs several controllers in parallel:

* `manilaController`: Talks to OpenStack API and checks if Manila service is provided.
  * If Manila service is found: 
    * It starts `manilaControllerSet`: Runs `csidriverset.Controller` that installs the Manila CSI driver itself.
    * It starts `nfsController`: Runs `csidriverset.Controller` that installs NFS CSI driver itself.
    * It creates `StorageClass` for each share type reported by Manila and periodically syncs them at least once per minute, in case a new share type appears in Manila.
    * It never removes StorageClass if Manila service or share type disappears. It may be temporary OpenStack ir Manila re-configuration hiccup.
  * If there is no Manila service, it marks the `ClusterCSIDriver` instance with `ManilaControllerDisabled: True` condition. It does not stop any CSI drivers started when Manila service was present! This allows pod to at least unmount their volumes. 
* `secretSyncController`: Syncs Secret provided by cloud-credentials-operator into a new Secret that is used by the CSI drivers. The drivers need OpenStack credentials in different format than provided by cloud-credentials-operator.

The operator is deployed and managed by another operator, the [Cluster Storage Operator](https://github.com/openshift/cluster-storage-operator). You can find the manifests that this operator uses [here](https://github.com/openshift/cluster-storage-operator/tree/master/assets/csidriveroperators/manila).

The operator makes few assumptions about the namespace where it runs:

* OpenStack cloud credentials are in Secret named `cloud-credentials` in the same namespace where the operator runs. The operator uses the credentials to check if Manila is present in the cluster and, since OpenStack does not allow any fine-grained access control, it lets the CSI driver to use the same credentials.
* If underlying OpenStack uses self-signed certificate, the operator expects the certificate is present in a ConfigMap named "cloud-provider-config" with key "ca-bundle.pem" in the namespace where it runs. Generally, it should be a copy of "openshift-config/cloud-provider-config" ConfigMap. It then uses the certificate to talk to OpenStack API.
* The operand (= the CSI driver) must run in the same namespace as the operator, for the same reason as above - it uses the same self-signed OpenStack certificate, if provided.
