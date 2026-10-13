package main

import (
	"context"
	"os"

	openstack_cinder "github.com/openshift/csi-operator/pkg/driver/openstack-cinder"
	"github.com/openshift/library-go/pkg/controller/controllercmd"
	"github.com/spf13/cobra"
	"k8s.io/component-base/cli"
	"k8s.io/utils/clock"

	"github.com/openshift/csi-operator/pkg/operator"
	"github.com/openshift/csi-operator/pkg/version"
)

func main() {
	command := NewOperatorCommand()
	code := cli.Run(command)
	os.Exit(code)
}

var guestKubeconfig *string

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
		// operator.RunOperator,
		runCSIDriverOperator,
		clock.RealClock{},
	).NewCommand()

	guestKubeconfig = ctrlCmd.Flags().String("guest-kubeconfig", "", "Path to the guest kubeconfig file. This flag enables hypershift integration.")

	ctrlCmd.Use = "start"
	ctrlCmd.Short = "Start the OpenStack Cinder CSI Driver Operator"

	cmd.AddCommand(ctrlCmd)

	return cmd
}

func runCSIDriverOperator(ctx context.Context, controllerConfig *controllercmd.ControllerContext) error {
	opConfig := openstack_cinder.GetOpenStackCinderOperatorConfig()
	return operator.RunOperator(ctx, controllerConfig, *guestKubeconfig, opConfig)
}
