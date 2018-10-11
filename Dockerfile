FROM openshift/origin-release:golang-1.10
COPY . /go/src/github.com/openshift/csi-operator/
RUN cd /go/src/github.com/openshift/csi-operator && \
    go build ./cmd/csi-operator

FROM centos:7
LABEL io.openshift.release.operator true

COPY --from=0 /go/src/github.com/openshift/csi-operator /usr/bin/

# TODO: add manifests:
# COPY deploy/image-references deploy/00-crd.yaml deploy/01-namespace.yaml deploy/03-openshift-rbac.yaml deploy/02-rbac.yaml deploy/04-operator.yaml /manifests/

RUN useradd csi-operator
USER csi-operator

ENTRYPOINT []
CMD ["/usr/bin/csi-operator"]
