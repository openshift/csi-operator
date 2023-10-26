all: build
.PHONY: all

GO_BUILD_BINDIR=bin/

# Include the library makefile
include $(addprefix ./vendor/github.com/openshift/build-machinery-go/make/, \
	golang.mk \
	targets/openshift/deps-gomod.mk \
	targets/openshift/images.mk \
)

test-unit: test-unit-aws-ebs

verify: verify-aws-ebs verify-generated-assets

verify-generated-assets: update-generated-assets
	git diff --exit-code
.PHONY: verify-generated-assets

test-unit-aws-ebs:
	cd legacy/aws-ebs-csi-driver-operator && $(MAKE) test-unit
.PHONY: test-unit-aws-ebs

verify-aws-ebs:
	cd legacy/aws-ebs-csi-driver-operator && $(MAKE) verify
.PHONY: verify-aws-ebs

update: update-generated-assets

update-generated-assets:
	hack/update-generated-assets.sh
.PHONY: update-generated-assets
