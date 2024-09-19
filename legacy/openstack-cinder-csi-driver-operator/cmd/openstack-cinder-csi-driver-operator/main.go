package main

import (
	"os"

	"github.com/spf13/cobra"

	"k8s.io/component-base/cli"

	"github.com/openshift/library-go/pkg/controller/controllercmd"

	"github.com/openshift/openstack-cinder-csi-driver-operator/pkg/operator"
	"github.com/openshift/openstack-cinder-csi-driver-operator/pkg/version"
)

func main() {
	command := NewOperatorCommand()
	code := cli.Run(command)
	os.Exit(code)
}

func NewOperatorCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "openstack-cinder-csi-driver-operator",
		Short: "OpenShift OpenStack Cinder CSI Driver Operator",
		Run: func(cmd *cobra.Command, args []string) {
			cmd.Help()
			os.Exit(1)
		},
	}

	ctrlCmd := controllercmd.NewControllerCommandConfig(
		"openstack-cinder-csi-driver-operator",
		version.Get(),
		operator.RunOperator,
	).NewCommand()
	ctrlCmd.Use = "start"
	ctrlCmd.Short = "Start the OpenStack Cinder CSI Driver Operator"

	cmd.AddCommand(ctrlCmd)

	return cmd
}
