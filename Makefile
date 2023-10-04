# Run tests
.PHONY: test
test: test-unit

.PHONY: verify
verify:
	cd legacy/aws-ebs-csi-driver-operator && $(MAKE) verify
test-unit:
	cd legacy/aws-ebs-csi-driver-operator && $(MAKE) test-unit
