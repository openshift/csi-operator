
# Image URL to use all building/pushing image targets
IMG ?= csi-operator:latest

BINDATA=pkg/generated/bindata.go
BINDATA_SRC=pkg/generated/manifests

E2EDATA=test/e2e/bindata.go
E2EDATA_SRC=test/e2e/manifests
all: build e2e

# Run tests
.PHONY: test
test:
	go test ./pkg/... ./cmd/... -coverprofile cover.out

# Build the binary
.PHONY: build
build: generate
	go build -o bin/csi-operator github.com/openshift/csi-operator/cmd/csi-operator

.PHONY: generate
generate:
	go-bindata -nometadata -pkg generated -prefix $(BINDATA_SRC) -o $(BINDATA) $(BINDATA_SRC)/...
	go-bindata -nometadata -pkg e2e -prefix $(E2EDATA_SRC) -o $(E2EDATA) $(E2EDATA_SRC)/...
	gofmt -s -w $(BINDATA)
	gofmt -s -w $(E2EDATA)

.PHONY: verify
verify:
	hack/verify-all.sh

.PHONY: test-e2e
# usage: KUBECONFIG=/var/run/kubernetes/admin.kubeconfig make test-e2e
test-e2e: generate
	go test ./test/e2e/... -kubeconfig=$(KUBECONFIG)  -root $(PWD) -globalMan deploy/prerequisites/01_crd.yaml -v

.PHONY: container
# Build the docker image
container: test
	docker build . -t ${IMG}
