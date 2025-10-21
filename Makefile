CURPATH=$(PWD)
BIN_PATH=$(CURPATH)/bin
YQ = $(BIN_PATH)/yq
YQ_VERSION = v4.47.1
export PATH := $(BIN_PATH):$(PATH)

all: build
.PHONY: all

GO_BUILD_BINDIR=bin

# Include the library makefile
include $(addprefix ./vendor/github.com/openshift/build-machinery-go/make/, \
	golang.mk \
	targets/openshift/deps-gomod.mk \
	targets/openshift/images.mk \
	targets/openshift/yq.mk \
)

# Bump OCP version in CSV and OLM metadata
#
# Example:
#   make metadata OCP_VERSION=4.20.0
metadata: ensure-yq
ifdef OCP_VERSION
	./hack/update-metadata.sh $(OCP_VERSION)
else
	./hack/update-metadata.sh
endif
.PHONY: metadata

verify: verify-generated-assets

verify-generated-assets: update-generated-assets
	git diff --exit-code
.PHONY: verify-generated-assets

update: update-generated-assets metadata

update-generated-assets:
	hack/update-generated-assets.sh
.PHONY: update-generated-assets
