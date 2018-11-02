# CSI driver deployment operator configuration

## Environment variables
* `KUBERNETES_CONFIG`: path to kubernetes configuration file to use when connecting to Kubernetes.
  If not set, the operator assumes that it runs as a pod and connects to Kubernetes using ServiceAccount token.
  
  Example for cluster started by `hack/local-up-cluster.sh`:
  ```sh
  KUBERNETES_CONFIG=/var/run/kubernetes/admin.kubeconfig csi-operator -v 5
  ```

## Command line arguments

```
  -config string
    	Path to configuration yaml file
```

There is no default value for `-config`.

`csi-operator` binary accepts the usual glog arguments:

```
  -alsologtostderr
    	log to standard error as well as files
  -log_backtrace_at value
    	when logging hits line file:N, emit a stack trace
  -log_dir string
    	If non-empty, write log files in this directory
  -logtostderr
    	log to standard error instead of files
  -stderrthreshold value
    	logs at or above this threshold go to stderr
  -v value
    	log level for V logs
  -vmodule value
    	comma-separated list of pattern=N settings for file-filtered logging
```

Note that logging to stderr is enabled by default so logs are visible in `kubectl logs`.

## Configuration file
Configuration file has yaml syntax. See [default-config.yaml](../pkg/generated/manifests/default-config.yaml)
for list of supported items and their default values. Note that a configuration file does not need to list
all supported fields, default values are used for missing fields.

It's expected that the configuration file is either baked into the image with the operator or projected into
the container as a ConfigMap volume and path to the file is passed via `-config` command line argument.

The configuration file allows tuning for various Kubernetes distributions and their versions. They may use
different path than `/var/lib/kubelet` or different images (or versions) for Kubernetes sidecar containers.

The operator currently does **not** reload the configuration file when it's updated on the filesystem. It needs
to be restarted! See issue #17.
