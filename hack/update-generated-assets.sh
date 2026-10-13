#!/bin/bash

set -e

REPO_ROOT="$(dirname $0)/.."

cd "${REPO_ROOT}"
GENERATOR="go run github.com/openshift/csi-operator/cmd/generator"
${GENERATOR} -path assets
