package cmd

import (
	"github.com/aws/aws-sdk-go/service/autoscaling"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/ecs"
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

	cmd.Flags().Int64("batch-size", ecsconst.MaxListableContainerInstances, "The number of instances drained at a once")

	replaceAutoScalingGroupInstancesCmd = cmd
}

func replaceAutoScalingGroupInstances(cmd *cobra.Command, args []string) error {
	name, _ := replaceAutoScalingGroupInstancesCmd.Flags().GetString("auto-scaling-group-name")
	cluster, _ := replaceAutoScalingGroupInstancesCmd.Flags().GetString("cluster")
	batchSize, _ := replaceAutoScalingGroupInstancesCmd.Flags().GetInt64("batch-size")

	sess, err := newSession()
	if err != nil {
		return newRuntimeError("failed to initialize a session: %w", err)
	}

	var asg *capacity.AutoScalingGroup
	asg, err = capacity.NewAutoScalingGroup(name, autoscaling.New(sess), ec2.New(sess))
	if err != nil {
		return newRuntimeError("failed to initialize a AutoScalingGroup: %w", err)
	}

	var drainer capacity.Drainer
	drainer, err = capacity.NewDrainer(cluster, batchSize, ecs.New(sess))
	if err != nil {
		return newRuntimeError("failed to initialize a Drainer: %w", err)
	}

	if err := asg.ReplaceInstances(drainer); err != nil {
		return newRuntimeError("failed to replace instances: %w", err)
	}
	return nil
}
