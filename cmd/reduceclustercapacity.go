package cmd

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	eventbridgetypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/spf13/cobra"
	"golang.org/x/xerrors"

	"github.com/abicky/ecsmec/internal/capacity"
	"github.com/abicky/ecsmec/internal/const/ecsconst"
)

var reduceClusterCapacityCmd *cobra.Command

const (
	queueNameForInterruptionWarnings = "ecsmec-ec2-spot-instance-interruption-warnings"
	ruleNameForInterruptionWarnings  = "ecsmec-forward-ec2-spot-instance-interruption-warnings"
)

func init() {
	cmd := &cobra.Command{
		Use:   "reduce-cluster-capacity",
		Short: "Reduce the cluster capacity safely",
		Long: `This command reduces the capacity of the specified cluster safely
that belong to the auto scaling group or spot fleet request.`,
		RunE: reduceClusterCapacity,
	}
	rootCmd.AddCommand(cmd)

	cmd.Flags().String("auto-scaling-group-name", "", "The name of the target `GROUP`")
	cmd.Flags().String("spot-fleet-request-id", "", "The ID of the target `REQUEST`")

	cmd.Flags().String("cluster", "default", "The name of the target `CLUSTER`")

	cmd.Flags().Int32("amount", 0, "The amount of the capacity to reduce (required)")
	cmd.MarkFlagRequired("amount")

	reduceClusterCapacityCmd = cmd
}

func reduceClusterCapacity(cmd *cobra.Command, args []string) error {
	id, _ := reduceClusterCapacityCmd.Flags().GetString("spot-fleet-request-id")
	name, _ := reduceClusterCapacityCmd.Flags().GetString("auto-scaling-group-name")
	cluster, _ := reduceClusterCapacityCmd.Flags().GetString("cluster")
	amount, _ := reduceClusterCapacityCmd.Flags().GetInt32("amount")

	if len(id) == 0 && len(name) == 0 {
		return errors.New("\"spot-fleet-request-id\" or \"auto-scaling-group-name\" is required")
	}
	if amount <= 0 {
		return errors.New("\"amount\" must be greater than 0")
	}

	cfg, err := newConfig(cmd.Context())
	if err != nil {
		return newRuntimeError("failed to initialize a session: %w", err)
	}

	drainer, err := capacity.NewDrainer(cluster, ecsconst.MaxListableContainerInstances, ecs.NewFromConfig(cfg))
	if err != nil {
		return newRuntimeError("failed to initialize a Drainer: %w", err)
	}

	if len(id) == 0 {
		asg, err := capacity.NewAutoScalingGroup(name, autoscaling.NewFromConfig(cfg), ec2.NewFromConfig(cfg))
		if err != nil {
			return newRuntimeError("failed to initialize a AutoScalingGroup: %w", err)
		}

		if err := asg.ReduceCapacity(cmd.Context(), amount, drainer); err != nil {
			return newRuntimeError("failed to reduce the cluster capacity: %w", err)
		}
	} else {
		sfr, err := capacity.NewSpotFleetRequest(id, ec2.NewFromConfig(cfg))
		if err != nil {
			return newRuntimeError("failed to initialize a SpotFleetRequest: %w", err)
		}

		sqsSvc := sqs.NewFromConfig(cfg)
		queueURL, queueArn, err := putSQSQueue(cmd.Context(), sqsSvc, queueNameForInterruptionWarnings)
		if err != nil {
			return newRuntimeError("failed to create a queue for interruption warnings: %w", err)
		}

		eventsSvc := eventbridge.NewFromConfig(cfg)
		targetID := "sqs"
		if err := putEventRule(cmd.Context(), eventsSvc, sqsSvc, ruleNameForInterruptionWarnings, targetID, queueURL, queueArn); err != nil {
			return newRuntimeError("failed to create an event rule for interruption warnings: %w", err)
		}

		if err := sfr.ReduceCapacity(cmd.Context(), amount, drainer, capacity.NewSQSQueuePoller(queueURL, sqsSvc)); err != nil {
			return newRuntimeError("failed to reduce the cluster capacity: %w", err)
		}

		if err := deleteEventRule(cmd.Context(), eventsSvc, ruleNameForInterruptionWarnings, targetID); err != nil {
			return newRuntimeError("failed to delete the event rule \"%s\": %w", ruleNameForInterruptionWarnings, err)
		}
		if err := deleteSQSQueue(cmd.Context(), sqsSvc, queueURL); err != nil {
			return newRuntimeError("failed to delete the SQS queue \"%s\": %w", queueNameForInterruptionWarnings, err)
		}
	}

	return nil
}

func putSQSQueue(ctx context.Context, svc *sqs.Client, name string) (string, string, error) {
	queue, err := svc.CreateQueue(ctx, &sqs.CreateQueueInput{
		QueueName: aws.String(name),
	})
	if err != nil {
		return "", "", xerrors.Errorf("failed to create the SQS queue \"%s\": %w", name, err)
	}

	attrs, err := svc.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		AttributeNames: []sqstypes.QueueAttributeName{
			"QueueArn",
		},
		QueueUrl: queue.QueueUrl,
	})
	if err != nil {
		return "", "", xerrors.Errorf("failed to get queue attributes of the queue \"%s\": %w", name, err)
	}

	return *queue.QueueUrl, attrs.Attributes["QueueArn"], nil
}

func deleteSQSQueue(ctx context.Context, svc *sqs.Client, queueURL string) error {
	_, err := svc.DeleteQueue(ctx, &sqs.DeleteQueueInput{
		QueueUrl: aws.String(queueURL),
	})
	return err
}

func putEventRule(ctx context.Context, eventsSvc *eventbridge.Client, sqsSvc *sqs.Client, ruleName, targetID, queueURL, queueArn string) error {
	rule, err := eventsSvc.PutRule(ctx, &eventbridge.PutRuleInput{
		EventPattern: aws.String("{\"detail-type\":[\"EC2 Spot Instance Interruption Warning\"],\"source\":[\"aws.ec2\"]}"),
		Name:         aws.String(ruleName),
	})
	if err != nil {
		return xerrors.Errorf("failed to create a rule for interruption warnings: %w", err)
	}

	_, err = sqsSvc.SetQueueAttributes(ctx, &sqs.SetQueueAttributesInput{
		Attributes: map[string]string{
			"Policy": fmt.Sprintf(`{
              "Version": "2012-10-17",
              "Statement": [
                {
                  "Effect": "Allow",
                  "Principal": {
                    "Service": "events.amazonaws.com"
                  },
                  "Action": "SQS:SendMessage",
                  "Resource": "%s",
                  "Condition": {
                    "ArnEquals": {
                      "AWS:SourceArn": "%s"
                    }
                  }
                }
              ]
		    }`, queueArn, *rule.RuleArn),
		},
		QueueUrl: aws.String(queueURL),
	})
	if err != nil {
		return xerrors.Errorf("failed to update the queue access policy for interruption warnings: %w", err)
	}

	_, err = eventsSvc.PutTargets(ctx, &eventbridge.PutTargetsInput{
		Rule: aws.String(ruleName),
		Targets: []eventbridgetypes.Target{
			{
				Id:  aws.String(targetID),
				Arn: aws.String(queueArn),
			},
		},
	})
	if err != nil {
		return xerrors.Errorf("failed to put a target for interruption warnings: %w", err)
	}

	return nil
}

func deleteEventRule(ctx context.Context, svc *eventbridge.Client, ruleName, targetID string) error {
	_, err := svc.RemoveTargets(ctx, &eventbridge.RemoveTargetsInput{
		Ids:  []string{targetID},
		Rule: aws.String(ruleName),
	})
	if err != nil {
		return xerrors.Errorf("failed to remove targets of the rule \"%s\": %w", ruleName, err)
	}

	_, err = svc.DeleteRule(ctx, &eventbridge.DeleteRuleInput{
		Force: true,
		Name:  aws.String(ruleName),
	})
	return err
}
