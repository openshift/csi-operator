# CSI driver deployment operator usage

## Requirements on CSI drivers

For purpose of this operator, a CSI driver consists of one or more *containers*. Typically, it's just a single container.

There are few requirements on the driver:

* The driver must **create CSI endpoint as UNIX socket somewhere inside the container with the driver**. If the driver is composed of several containers, it must be **created in the first container**. In addition, it should be created in an empty directory. `/run/csi/csi.sock` is a good choice. The whole directory with the socket will be exported out of the container by the operator via HostPath and EmptyDir volumes to all consumers of the socket.  

* Driver's `Probe()` CSI call should be reasonably fast if liveness probe is configured in the operator. It will be called periodically and its failure or timeout will result in restart of the first container. Drivers may implement their own liveness probe, operator's liveness probe is only optional and it's off by default. 
  
## CSIDriverDeployment API

See [types.go](../pkg/apis/csidriver/v1alpha1/types.go) for complete API reference. Here we describe *how* to use the API to install a driver.

`CSIDriverDeployment` API object describes deployment of one CSI driver. Cluster admin may install several CSI drivers using the operator by creating `CSIDriverDeployment` instance for each driver. 

`CSIDriverDeployment` is a namespaced object. The operator deploys the driver into the namespace where the `CSIDriverDeployment` instance exists. We recommend installing each driver into a dedicated namespace.

### `driverName`
`driverName` must be name of the CSI driver. That means the driver must return this name in its `GetPluginInfoResponse`. We recommend to use the driver name also as `CSIDriverDeployment` instance name, however, it's not strictly required.

Example of `CSIDriverDeployment` using a HostPath sample driver:
```yaml
apiVersion: csidriver.storage.openshift.io/v1alpha1
kind: CSIDriverDeployment
metadata:
  name: csi-hostpath
spec:
  driverName: csi-hostpath
```

### `driverPerNodeTemplate`
As written above, a CSI driver is one or more containers. The operator must know *what* containers to run and *how*. All this is captured in `driverPerNodeTemplate` that specifies a complete Pod template with the driver that runs on every node and runs all Node Plugin code. Kubernetes won't call any Controller Plugin CSI calls there.

Put a complete pod template with the driver, with all the volumes it needs (ConfigMap, Secrets, ...), its command line arguments, env. variables and so on. This pod template can consist of multiple containers, however, the first one should expose the driver socket somewhere inside it. Typically, the container with the driver runs as `privileged` to be able to mount volumes.

Continuing example with HostPath driver:
```yaml
  driverPerNodeTemplate:
    spec:
      containers:
      - args:
        - --v=5
        - --endpoint=unix:///run/csi/csi.sock
        - --nodeid=$(KUBE_NODE_NAME)
        env:
        - name: KUBE_NODE_NAME
          valueFrom:
            fieldRef:
              apiVersion: v1
              fieldPath: spec.nodeName
        image: quay.io/k8scsi/hostpathplugin:v0.2.0
        name: csi-driver
        securityContext:
          privileged: true
        volumeMounts:
        - name: hostpath-root
          mountPath: /tmp
      volumes:
      - name: hostpath-root
        # This volume is needed by the hostpath driver itself.
        # Other drivers probably don't need to use it.
        hostPath:
          path: /tmp
          type: Directory
```

#### Modifications done by the operator
The operator modifies `driverPerNodeTemplate` this way:
* Add HostPath volume to the first container in the pod template. It points to `/var/lib/kubelet/<csi-driver-name>/` on the host and the directory with the CSI socket in the container. This way, the operator exposes the CSI driver socket to kubelet running on the host. The driver should not notice any difference, it creates the socket in a directory in its own container and does not need to worry about exposing it to the host.
* Add HostPath volume with Bidirectional mount propagation to the first container in the pod template. It points to `/var/lib/kubelet/` on the host and to `/var/lib/kubelet/` in the container. This way, the driver can mount volumes so kubelet can see them and use them in workload containers. 
* Add CSI driver registrar container as sidecar container. This container registers CSI driver socket to kubelet, so kubelet knows where the socket is and calls it directly.
* If no ServiceAccount is configured in the template, create a new ServiceAccount, give it privileges to run external attacher, external provisioner and driver registrar and add it to the template.
* Optionally, set liveness probe:
  * Add CSI liveness probe container as sidecar container. This container serves as liveness probe endpoint of the driver and it issues `Probe()` calls to the driver.
  * Add liveness probe to the first container of the driver. It calls the endpoint exposed by the liveness probe sidecar container in configured intervals. If the call fails 3 times, Kubernetes assumes that the driver is broken and it will restart the first driver container.

The operator will run the modified pod template on every node in the cluster using a DaemonSet. Currently it's not possible to limit the nodes in any way. 

### `driverControllerTemplate`
This is a template of controller part of the CSI driver. Typically, it's the same as `driverPerNodeTemplate`, except it does not need to run as privileged.

Continuing example with HostPath driver:
```yaml
  driverControllerTemplate:
    spec:
      containers:
      - args:
        - --v=5
        - --endpoint=unix:///run/csi/csi.sock
        - --nodeid=$(KUBE_NODE_NAME)
        env:
        - name: KUBE_NODE_NAME
          valueFrom:
            fieldRef:
              apiVersion: v1
              fieldPath: spec.nodeName
        image: quay.io/k8scsi/hostpathplugin:v0.2.0
        name: csi-driver
        volumeMounts:
        - name: hostpath-root
          mountPath: /tmp
        # Typical CSI drivers don't need to run privileged here,
        # this HostPath CSI driver needs to.
        securityContext:
          privileged: true
      volumes:
      # This volume is needed by the hostpath driver itself.
      # Other drivers probably don't need to use it.
      - name: hostpath-root
        hostPath:
          path: /tmp
          type: Directory
```

#### Modifications done by the operator
The operators modifies `driverControllerTemplate` this way:
* Add EmptyDir volume to the first container in the pod template. It will be shared with all other containers added by the operator in this pod. Again, the driver should not notice any difference, it creates the socket in a directory in its own container and does not need to worry about exposing it to the other container.
* Add CSI external provisioner container as sidecar container. This container does dynamic provisioning and deletion of volumes.
* Add CSI external attacher container as sidecar container. This container attaches/detaches volumes to/from nodes. This container is running even if the volume itself does not support attach/detach (ControllerPublishVolume/ControllerUnpublishVolume CSI calls).
* If no ServiceAccount is configured in the template, create a new ServiceAccount, give it privileges to run external attacher, external provisioner and driver registrar and add it to the template.
* Set liveness probe:
  * Add CSI liveness probe container as sidecar container. This container serves as liveness probe endpoint of the driver and it issues `Probe()` calls to the driver.
  * Add liveness probe to the first container of the driver. It calls the endpoint exposed by the liveness probe sidecar container every minute. If the call fails 3 times, Kubernetes will restart the first driver container.

The operator will run the modified pod template in a Deployment. If the operator is configured with infrastructure nodes, the Deployment then runs only on these infrastructure nodes.

### `driverSocket`
This is path to the UNIX domain socket created by the driver. The operator needs to know its location so it can wrap it with HostPath or EmptyDir volume and expose the socket to the right place.

Both drivers in `driverControllerTemplate` and `driverPerNodeTemplate` must expose the socket to the same path!
 
Continuing example with HostPath driver:
```yaml
  driverSocket: /run/csi/csi.sock
```

### `nodeUpdateStrategy`
* `Rolling`: Use this strategy if your driver running on all nodes can be safely restarted without destroying mounts they created. Any update of `driverPerNodeTemplate` will do rolling update of DaemonSet that runs the driver on each node. Pods with the old template will be killed and new pods with the updated template will be started.

* `OnDelete`: Use this strategy ff your driver can't be restarted, for example because it uses fuse daemons and killing a fuse daemon effectively kills all mounts created by the driver. The operator updates the DaemonSet with `OnDelete` strategy that does not restart the pods. You're on your own when updating the nodes - we recommend to go through all nodes one by one, drain a node (which unmounts all volumes), kill driver pod running on the node (nothing should be mounted at that time) and DaemonSet will start a new pod with updated template.


### `storageClassTemplates`
Put list of all the storage classes you want to be created by the operator when the driver is deployed. It can even create a default storage class, however, keep in mind that only one class can be default and the operator won't check if there is any other default class in the cluster (such check is racy).

```yaml
  storageClassTemplates:
    - metadata:
        name: my-default-class
      default: true
      # HostPath driver does not support any parameters, so an example commented out:
      # parameters:
      #   foo: bar
    - metadata:
        name: my-non-default-class
      default: false
      # parameters:
      #   foo: baz
```

### `managementState`
This field is `Managed` by default. That means that the operator actively manages all objects the operator creates - Deployment, DaemonSet, ServiceAccount, StorageClasses and so on - and prevents any changes in them. If you change e.g. the Deployment e.g. increase number of its replicas, the operator with detect that and overwrite your changes. It owns the objects. Similarly, if you delete the Deployment (or any other object), the operator will restore it. 
 
When set to `Unmanaged`, the operator won't manage the objects any longer. It can be used to "turn off" the operator actions on a particular `CSIDriverDeployment` instance when the operator does something wrong. You may fix the objects it manages and the operator won't change them in any way.

Note that this field is per `CSIDriverDeployment` instance - you can have some drivers `Managed` and others `Unmanaged`.

## Driver installation
We recommend to run each CSI driver in separate namespace. So, create a namespace and `CSIDriverDeployment` instance in it. The operator creates a ServiceAccount in the namespace, bind all necessary roles to it so the account can run external attacher and external provisioner and run the DaemonSet and Deployment and create configured StorageClasses.

## Update
CSI drivers can be updated by changing `CSIDriverDeployment` instance, for example changing container images in `driverControllerTemplate` and `driverPerNodeTemplate`. Deployment with controller parts will be updated with Rolling update strategy. DaemonSet with pods running on every node will be updated according to `nodeUpdateStrategy`.

Note that **every** change in these templates will update the Deployment and DaemonSet. Be careful!

Similarly, adding or removing items in `storageClassTemplates` will add or delete StorageClasses.

## Un-installation
Just delete `CSIDriverDeployment` instance. Note that this will forcefully delete CSI driver from the cluster. Attached or mounted volumes will remain attached / mounted forever, because there is no driver that would clean them up. The operator will automatically remove all objects it manages - Deployment, DaemonSet, ServiceAccount, all StorageClasses it created and so on.

**All pods that use storage provided by the driver may break. Data can get corrupted. Make sure that no application uses the storage before deleting a driver!**

## Monitoring
`CSIDriverDeployment` exposes `status` with these fields:

* `conditions`:

  * `Available`: This condition gets `true` when Deployment and DaemonSet created by the operator has expected number of replicas, i.e. when everything is happily running. This condition can get `false` temporarily during installation or update of the driver. When it's `false` for too long, check the condition message and DaemonSet or Deployment health.

  * `SyncSuccessful`: This condition reports if the last change in `CSIDriverDeployment` was processed correctly. It can get false when the operator either failed to parse the instance or failed to create some API objects. Some errors heal themselves, but check the condition message if the condition is `false` for a longer period. The operator won't do any actions when it can't parse the object!

* `observedGeneration`: This is the last generation of CSIDriverDeployment that was successfully processed by the operator. Something is wrong if it's different than the current `metadata/generation`. `SyncSuccessful` condition should be `false` in that case.

* `children`: This is internal field used for the operator bookkeeping (keeping track of Deployment and DaemonSet generations) and has little use to anyone else.

TODO: Prometheus metrics

