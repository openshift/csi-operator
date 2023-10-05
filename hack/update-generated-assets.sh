#!/bin/bash

set -e

REPO_ROOT="$(dirname $0)/.."

cd "${REPO_ROOT}"
GENERATOR="go run github.com/openshift/csi-operator/cmd/generator"

# TODO: loop over flavours and drivers
${GENERATOR} -flavour standalone -path assets/overlays/aws-ebs/generated/standalone
${GENERATOR} -flavour hypershift -path assets/overlays/aws-ebs/generated/hypershift
