# Build the manager binary
FROM golang:1.10.3 as builder

# Copy in the go src
WORKDIR /go/src/github.com/openshift/csi-operator
COPY pkg/    pkg/
COPY cmd/    cmd/
COPY vendor/ vendor/

# Build
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -o csi-operator github.com/openshift/csi-operator/cmd/manager

# Copy the controller-manager into a thin image
FROM centos:latest
WORKDIR /root/
COPY --from=builder /go/src/github.com/openshift/csi-operator/csi-operator .
ENTRYPOINT ["./csi-operator"]
