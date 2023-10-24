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

verify: verify-aws-ebs

test-unit-aws-ebs:
	cd legacy/aws-ebs-csi-driver-operator && $(MAKE) test-unit
.PHONY: test-unit-aws-ebs

verify-aws-ebs:
	cd legacy/aws-ebs-csi-driver-operator && $(MAKE) verify
.PHONY: verify-aws-ebs

update:
	hack/update-generated-assets.sh
