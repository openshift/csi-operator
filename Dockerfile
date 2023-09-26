FROM registry.svc.ci.openshift.org/openshift/release:golang-1.14 AS builder
WORKDIR /go/src/github.com/openshift/aws-ebs-csi-driver-operator
COPY . .
RUN make

FROM registry.svc.ci.openshift.org/openshift/origin-v4.0:base
COPY --from=builder /go/src/github.com/openshift/aws-ebs-csi-driver-operator/aws-ebs-csi-driver-operator /usr/bin/
ENTRYPOINT ["/usr/bin/aws-ebs-csi-driver-operator"]
LABEL io.k8s.display-name="OpenShift AWS EBS CSI Driver Operator" \
	io.k8s.description="The AWS EBS CSI Driver Operator installs and maintains the AWS EBS CSI Driver on a cluster."
