# AWS CSI test volume creator

`volume-create` creates AWS EFS volumes and AWS S3 Files test infrastructure for CSI tests. It
combines the supported operations of the existing EFS and S3 Files helpers under one command.

Build the binary or image with:

```shell
go build -o bin/volume-create ./cmd/volume-create
podman build -f Dockerfile.volume_create -t volume-create:latest .
```

The command uses credentials from the cluster by default. Use `--local-aws-creds` to use the
normal local AWS credential chain, for example on STS clusters.

Each operation accepts `--kubeconfig`, which overrides `KUBECONFIG` and the default client-go
configuration discovery. Without it, the command loads `KUBECONFIG` (including merged files)
or `~/.kube/config`, falling back to in-cluster credentials when no configuration is available.

S3 Files requires an installed EFS CSI driver whose `mount.s3files` helper reads
`/var/run/aws/auth/credentials`. The tool grants client permissions to the CSI controller
and node identities, not the EC2 instance roles. This supports both pod mounts and
controller access-point root-directory cleanup without instance-role
`AmazonS3FilesClientFullAccess`. Other instance-role permissions remain unchanged.

```shell
export KUBECONFIG=/path/to/kubeconfig

# Create an EFS volume.
STORAGECLASS_LOCATION=sc.yaml MANIFEST_LOCATION=manifest.yaml \
  ./bin/volume-create --local-aws-creds efs create --kubeconfig "$KUBECONFIG"
./bin/volume-create --local-aws-creds efs describe --kubeconfig "$KUBECONFIG"

# Create, inspect, or remove S3 Files infrastructure.
./bin/volume-create --local-aws-creds s3 create --kubeconfig "$KUBECONFIG"
./bin/volume-create --local-aws-creds s3 describe --kubeconfig "$KUBECONFIG"
./bin/volume-create --local-aws-creds s3 delete --kubeconfig "$KUBECONFIG" --force
```

The EFS and S3 `create` commands accept `--single-zone <zone>`. `--force` is accepted only by
`s3 delete`; it bypasses the check for existing S3 Files workloads before cleanup. The image
includes EFS descriptors at `/usr/share/aws-efs-csi-driver/` and S3 Files descriptors at
`/usr/share/aws-s3files-csi-driver/`.
