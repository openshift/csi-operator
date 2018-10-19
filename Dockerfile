FROM openshift/origin-release:golang-1.10
COPY . /go/src/github.com/openshift/csi-operator/
RUN cd /go/src/github.com/openshift/csi-operator && \
    go build ./cmd/csi-operator

FROM centos:7
LABEL io.openshift.release.operator true

COPY --from=0 /go/src/github.com/openshift/csi-operator /usr/bin/

COPY deploy/openshift/image-references deploy/prerequisites/*.yaml /manifests/
COPY deploy/operator.yaml /manifests/99_operator.yaml

RUN useradd csi-operator
USER csi-operator

ENTRYPOINT ["/usr/bin/csi-operator"]
