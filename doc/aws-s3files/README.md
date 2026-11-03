# AWS S3 Files CSI test infrastructure

`create-s3files-volume` is a development and CI helper for the AWS EFS CSI driver. It creates
an S3 bucket, S3 Files file system, mount targets, security group, and the IAM permissions needed
to dynamically provision `s3files-ap` volumes.

The helper is a proof of concept. It must only be used on a dedicated test cluster because the
`delete` operation deletes the bucket and all of its object versions.

## Build

```shell
go build -o bin/create-s3files-volume ./cmd/create-s3files-volume
podman build -f Dockerfile.create_s3files -t create-s3files-volume:latest .
```

The helper uses the AWS Go SDK directly and does not require the AWS CLI in its image.

## Create test infrastructure

The default credential source is the cluster's `kube-system/aws-creds` secret. On STS clusters
or when running the command locally, pass `--local-aws-creds` to use the normal local AWS
credential chain.

```shell
export KUBECONFIG=/path/to/kubeconfig
export STORAGECLASS_LOCATION="$PWD/s3files-sc.yaml"
export MANIFEST_LOCATION="$PWD/s3files-manifest.yaml"

./bin/create-s3files-volume create \
  --kubeconfig "$KUBECONFIG" \
  --namespace openshift-cluster-csi-drivers \
  --local-aws-creds
```

Apply the generated StorageClass and run the CSI suite with the generated manifest:

```shell
oc apply -f "$STORAGECLASS_LOCATION"
TEST_CSI_DRIVER_FILES="$MANIFEST_LOCATION" openshift-tests run openshift/csi
```

The EFS CSI controller and node credential secrets must be present in
`openshift-cluster-csi-drivers`: `aws-efs-cloud-credentials` and
`node-aws-efs-cloud-credentials`.

## Status

Use `status` to describe the infrastructure created for the current cluster without changing it:

```shell
./bin/create-s3files-volume status \
  --kubeconfig "$KUBECONFIG" \
  --namespace openshift-cluster-csi-drivers \
  --local-aws-creds
```

## Delete

Delete PVCs using the generated StorageClass before deletion. The `delete` operation only removes S3 Files file
systems in this helper's deterministic bucket that are tagged
`kubernetes.io/cluster/<infrastructure-name>=owned`. It refuses to proceed while an EFS CSI
PersistentVolume references one of those file systems or any PVC uses `s3files-sc`, reporting the
affected resources. Use `--force` only to bypass this Kubernetes workload check. The command then
removes the file system and access points, mount targets, security group, helper IAM role and
policies, and all object versions in the test bucket.

```shell
./bin/create-s3files-volume delete \
  --kubeconfig "$KUBECONFIG" \
  --namespace openshift-cluster-csi-drivers \
  --local-aws-creds
```
