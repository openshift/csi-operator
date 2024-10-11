package main

import (
	"context"
	"fmt"
	"os"

	"github.com/openshift/csi-operator/pkg/driver/aws-ebs"
	"github.com/openshift/library-go/pkg/controller/controllercmd"
	"github.com/spf13/cobra"
	"k8s.io/component-base/cli"
	"k8s.io/klog/v2"

	configclient "github.com/openshift/client-go/config/clientset/versioned"
	"github.com/openshift/csi-operator/pkg/operator"
	"github.com/openshift/csi-operator/pkg/version"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
)

func main() {
	command := NewOperatorCommand()
	code := cli.Run(command)
	os.Exit(code)
}

var guestKubeconfig *string

func NewOperatorCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "aws-ebs-csi-driver-operator",
		Short: "OpenShift AWS EBS CSI Driver Operator",
		Run: func(cmd *cobra.Command, args []string) {
			cmd.Help()
			os.Exit(1)
		},
	}

	ctrlCmd := controllercmd.NewControllerCommandConfig(
		"aws-ebs-csi-driver-operator",
		version.Get(),
		// operator.RunOperator,
		runCSIDriverOperator,
	).NewCommand()

	guestKubeconfig = ctrlCmd.Flags().String("guest-kubeconfig", "", "Path to the guest kubeconfig file. This flag enables hypershift integration.")

	ctrlCmd.Use = "start"
	ctrlCmd.Short = "Start the AWS EBS CSI Driver Operator"

	cmd.AddCommand(ctrlCmd)

	return cmd
}

func runCSIDriverOperator(ctx context.Context, controllerConfig *controllercmd.ControllerContext) error {
	klog.Info("Starting AWS EBS CSI Driver Operator")

	opConfig := aws_ebs.GetAWSEBSOperatorConfig()

	configClient, err := configclient.NewForConfig(controllerConfig.KubeConfig)
	if err != nil {
		klog.Errorf("Failed to create config client: %v", err)
		return fmt.Errorf("failed to create config client: %v", err)
	}

	coreClient, err := corev1.NewForConfig(controllerConfig.KubeConfig)
	if err != nil {
		klog.Errorf("Failed to create core client: %v", err)
		return fmt.Errorf("failed to create core client: %v", err)
	}

	ebsTagsController, err := aws_ebs.NewEBSVolumeTagController(configClient, coreClient)
	if err != nil {
		klog.Errorf("Failed to create EBS volume tag controller: %v", err)
		return fmt.Errorf("failed to create EBS volume tag controller: %v", err)
	}

	go ebsTagsController.Run(ctx)

	klog.Info("EBS Volume Tag Controller is running")

	return operator.RunOperator(ctx, controllerConfig, *guestKubeconfig, opConfig)
}
