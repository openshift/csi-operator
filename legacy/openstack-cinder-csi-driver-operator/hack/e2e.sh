#!/bin/bash

set -e

REPO_ROOT="$(dirname $0)/.."

# Prepare openshift-tests arguments for log output
ADDITIONAL_TEST_ARGS=""
if [ -n "${ARTIFACT_DIR}" ]; then
    mkdir -p ${ARTIFACT_DIR}
    ADDITIONAL_TEST_ARGS="-o ${ARTIFACT_DIR}/e2e.log --junit-dir ${ARTIFACT_DIR}/junit"
fi

# Run openshift-tests
TEST_CSI_DRIVER_FILES=${REPO_ROOT}/test/e2e/manifest.yaml openshift-tests run openshift/csi $ADDITIONAL_TEST_ARGS
