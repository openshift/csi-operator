FROM scratch
LABEL operators.operatorframework.io.bundle.mediatype.v1=registry+v1
LABEL operators.operatorframework.io.bundle.manifests.v1=manifests/
LABEL operators.operatorframework.io.bundle.metadata.v1=metadata/
LABEL operators.operatorframework.io.bundle.package.v1=smb-csi-driver-operator
LABEL operators.operatorframework.io.bundle.channels.v1=stable
LABEL operators.operatorframework.io.bundle.channel.default.v1=stable
COPY manifests/stable/smb-csi-driver-operator.clusterserviceversion.yaml /manifests/smb-csi-driver-operator.clusterserviceversion.yaml
COPY metadata/annotations.yaml /metadata/annotations.yaml
