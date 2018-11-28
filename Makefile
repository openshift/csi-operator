# Image URL to use all building/pushing image targets
IMG ?= csi-operator:latest

CONTROLLER_MANIFESTS=pkg/generated/bindata.go
CONTROLLER_MANIFESTS_SRC=pkg/generated/manifests
E2E_MANIFESTS=test/e2e/bindata.go
E2E_MANIFESTS_SRC=test/e2e/manifests

BINDIR=bin
BINDATA=$(BINDIR)/go-bindata

all: build

# Run tests
.PHONY: test
test:
	go test ./pkg/... ./cmd/... -coverprofile cover.out

# Build the binary
.PHONY: build
build: generate
	go build -o $(BINDIR)/csi-operator github.com/openshift/csi-operator/cmd/csi-operator

.PHONY: generate
generate: $(BINDATA)
	$(BINDATA) -pkg generated -prefix $(CONTROLLER_MANIFESTS_SRC) -o $(CONTROLLER_MANIFESTS) $(CONTROLLER_MANIFESTS_SRC)/...
	gofmt -s -w $(CONTROLLER_MANIFESTS)
	$(BINDATA) -pkg e2e -prefix $(E2E_MANIFESTS_SRC) -o $(E2E_MANIFESTS) $(E2E_MANIFESTS_SRC)/...
	gofmt -s -w $(E2E_MANIFESTS)

.PHONY: verify
verify:
	hack/verify-all.sh

.PHONY: test-e2e
# usage: KUBECONFIG=/var/run/kubernetes/admin.kubeconfig make test-e2e
test-e2e: generate
	go test -v ./test/e2e/... -kubeconfig=$(KUBECONFIG)  -root $(PWD) -globalMan deploy/prerequisites/01_crd.yaml

.PHONY: container
# Build the docker image
container: test
	docker build . -t ${IMG}

$(BINDATA):
	go build -o $(BINDATA) ./vendor/github.com/jteeuwen/go-bindata/go-bindata
