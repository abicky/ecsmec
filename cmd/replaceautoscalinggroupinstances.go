package cmd

import (
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/spf13/cobra"

	"github.com/abicky/ecsmec/internal/capacity"
	"github.com/abicky/ecsmec/internal/const/ecsconst"
)

var replaceAutoScalingGroupInstancesCmd *cobra.Command

func init() {
	cmd := &cobra.Command{
		Use:   "replace-auto-scaling-group-instances",
		Short: "Replace container instances",
		Long: `This command replaces container instances that belong to the specified
auto scaling group and are launched before the time when this command
launches new ones.`,
		RunE: replaceAutoScalingGroupInstances,
	}
	rootCmd.AddCommand(cmd)

	cmd.Flags().String("auto-scaling-group-name", "", "The name of the target `GROUP` (required)")
	cmd.MarkFlagRequired("auto-scaling-group-name")

	cmd.Flags().String("cluster", "default", "The name of the target `CLUSTER`")

	cmd.Flags().Int32("batch-size", ecsconst.MaxListableContainerInstances, "The number of instances drained at a once")

	replaceAutoScalingGroupInstancesCmd = cmd
}

func replaceAutoScalingGroupInstances(cmd *cobra.Command, args []string) error {
	name, _ := replaceAutoScalingGroupInstancesCmd.Flags().GetString("auto-scaling-group-name")
	clusterName, _ := replaceAutoScalingGroupInstancesCmd.Flags().GetString("cluster")
	batchSize, _ := replaceAutoScalingGroupInstancesCmd.Flags().GetInt32("batch-size")

	cfg, err := newConfig(cmd.Context())
	if err != nil {
		return newRuntimeError("failed to initialize a session: %w", err)
	}

	asg, err := capacity.NewAutoScalingGroup(name, autoscaling.NewFromConfig(cfg), ec2.NewFromConfig(cfg))
	if err != nil {
		return newRuntimeError("failed to initialize a AutoScalingGroup: %w", err)
	}

	ecsSvc := ecs.NewFromConfig(cfg)
	drainer, err := capacity.NewDrainer(clusterName, batchSize, ecsSvc)
	if err != nil {
		return newRuntimeError("failed to initialize a Drainer: %w", err)
	}

	if err := asg.ReplaceInstances(cmd.Context(), drainer, capacity.NewCluster(clusterName, ecsSvc)); err != nil {
		return newRuntimeError("failed to replace instances: %w", err)
	}
	return nil
}
