//go:build tools
// +build tools

package dependencymagnet

import (
	_ "github.com/openshift/build-machinery-go"
	_ "github.com/openshift/library-go/pkg/operator/apiserver/controller/workload"
	_ "github.com/openshift/library-go/pkg/operator/status"
)
