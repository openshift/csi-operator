
# Image URL to use all building/pushing image targets
IMG ?= csi-operator:canary

all: build

# Run tests
test: fmt vet
	go test ./pkg/... ./cmd/... -coverprofile cover.out

# Build the binary
build:
	go build -o bin/csi-operator github.com/openshift/csi-operator2/cmd/csi-operator

# Run go fmt against code
fmt:
	go fmt ./pkg/... ./cmd/...

# Run go vet against code
vet:
	go vet ./pkg/... ./cmd/...

# Build the docker image
docker-build: test
	docker build . -t ${IMG}
	@echo "updating kustomize image patch file for manager resource"
	sed -i'' -e 's@image: .*@image: '"${IMG}"'@' ./config/default/manager_image_patch.yaml

# Push the docker image
docker-push:
	docker push ${IMG}
