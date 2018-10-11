
# Image URL to use all building/pushing image targets
IMG ?= csi-operator:latest

all: build

# Run tests
test: fmt vet
	go test ./pkg/... ./cmd/... -coverprofile cover.out

# Build the binary
build: fmt
	go build -o bin/csi-operator github.com/openshift/csi-operator/cmd/csi-operator

# Run go fmt against code
fmt:
	go fmt ./pkg/... ./cmd/...

# Run go vet against code
vet:
	go vet ./pkg/... ./cmd/...

# Build the docker image
container: test
	docker build . -t ${IMG}
