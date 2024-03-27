all: build
.PHONY: all

GO_BUILD_BINDIR=bin/

# Include the library makefile
include $(addprefix ./vendor/github.com/openshift/build-machinery-go/make/, \
	golang.mk \
	targets/openshift/deps-gomod.mk \
	targets/openshift/images.mk \
)

verify: verify-generated-assets

verify-generated-assets: update-generated-assets
	git diff --exit-code
.PHONY: verify-generated-assets

update: update-generated-assets

update-generated-assets:
	hack/update-generated-assets.sh
.PHONY: update-generated-assets
