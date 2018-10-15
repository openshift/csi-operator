
# Image URL to use all building/pushing image targets
IMG ?= csi-operator:latest

all: build

# Run tests
test:
	go test ./pkg/... ./cmd/... -coverprofile cover.out

# Build the binary
build:
	go build -o bin/csi-operator github.com/openshift/csi-operator/cmd/csi-operator

verify:
	hack/verify-all.sh

# Build the docker image
container: test
	docker build . -t ${IMG}
