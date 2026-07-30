package main

import (
	"context"
	"os"
	"time"

	aws_efs "github.com/openshift/csi-operator/pkg/driver/aws-efs"
	"github.com/openshift/library-go/pkg/controller/controllercmd"
	"github.com/spf13/cobra"
	"k8s.io/component-base/cli"
	"k8s.io/klog/v2"
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
		Use:   "aws-efs-csi-driver-operator",
		Short: "OpenShift AWS EFS CSI Driver Operator",
		Run: func(cmd *cobra.Command, args []string) {
			cmd.Help()
			os.Exit(1)
		},
	}

	ctrlCmd := controllercmd.NewControllerCommandConfig(
		"aws-efs-csi-driver-operator",
		version.Get(),
		runCSIDriverOperator,
		clock.RealClock{},
	).NewCommand()

	guestKubeconfig = ctrlCmd.Flags().String("guest-kubeconfig", "", "Path to the guest kubeconfig file. This flag enables hypershift integration.")

	// Read the cluster TLS profile and write a GenericOperatorConfig file
	// that controllercmd will use to configure its HTTPS serving endpoint.
	// This is an OLM-managed operator so it must read the APIServer CR
	// itself — CVO/CSO do not inject TLS config.
	originalPreRunE := ctrlCmd.PersistentPreRunE
	ctrlCmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if originalPreRunE != nil {
			if err := originalPreRunE(cmd, args); err != nil {
				return err
			}
		}

		ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
		defer cancel()

		configPath, err := operator.WriteOperatorTLSConfig(ctx, "aws-efs-csi-driver-operator")
		if err != nil {
			klog.Warningf("Failed to write TLS config, continuing with defaults: %v", err)
			return nil
		}
		if configPath != "" {
			if err := cmd.Flags().Set("config", configPath); err != nil {
				klog.Warningf("Failed to set config flag: %v", err)
			}
		}
		return nil
	}

	ctrlCmd.Use = "start"
	ctrlCmd.Short = "Start the AWS EFS CSI Driver Operator"

	cmd.AddCommand(ctrlCmd)

	return cmd
}

func runCSIDriverOperator(ctx context.Context, controllerConfig *controllercmd.ControllerContext) error {
	opConfig := aws_efs.GetAWSEFSOperatorConfig()
	return operator.RunOperator(ctx, controllerConfig, *guestKubeconfig, opConfig)
}
