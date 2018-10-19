
# Image URL to use all building/pushing image targets
IMG ?= csi-operator:latest

BINDATA=pkg/generated/bindata.go
BINDATA_SRC=pkg/generated/manifests

all: build

# Run tests
test:
	go test ./pkg/... ./cmd/... -coverprofile cover.out

# Build the binary
build: generate
	go build -o bin/csi-operator github.com/openshift/csi-operator/cmd/csi-operator

generate:
	go-bindata -nometadata -pkg generated -prefix $(BINDATA_SRC) -o $(BINDATA) $(BINDATA_SRC)/...
	gofmt -s -w $(BINDATA)

verify:
	hack/verify-all.sh

# Build the docker image
container: test
	docker build . -t ${IMG}
