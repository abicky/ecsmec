package cmd

import (
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/autoscaling"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/ecs"
	"github.com/aws/aws-sdk-go/service/eventbridge"
	"github.com/aws/aws-sdk-go/service/sqs"
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

	cmd.Flags().Int64("amount", 0, "The amount of the capacity to reduce (required)")
	cmd.MarkFlagRequired("amount")

	reduceClusterCapacityCmd = cmd
}

func reduceClusterCapacity(cmd *cobra.Command, args []string) error {
	id, _ := reduceClusterCapacityCmd.Flags().GetString("spot-fleet-request-id")
	name, _ := reduceClusterCapacityCmd.Flags().GetString("auto-scaling-group-name")
	cluster, _ := reduceClusterCapacityCmd.Flags().GetString("cluster")
	amount, _ := reduceClusterCapacityCmd.Flags().GetInt64("amount")

	if len(id) == 0 && len(name) == 0 {
		return errors.New("\"spot-fleet-request-id\" or \"auto-scaling-group-name\" is required")
	}
	if amount <= 0 {
		return errors.New("\"amount\" must be greater than 0")
	}

	sess, err := newSession()
	if err != nil {
		return newRuntimeError("failed to initialize a session: %w", err)
	}

	var drainer capacity.Drainer
	drainer, err = capacity.NewDrainer(cluster, ecsconst.MaxListableContainerInstances, ecs.New(sess))
	if err != nil {
		return newRuntimeError("failed to initialize a Drainer: %w", err)
	}

	if len(id) == 0 {
		asg, err := capacity.NewAutoScalingGroup(name, autoscaling.New(sess), ec2.New(sess))
		if err != nil {
			return newRuntimeError("failed to initialize a AutoScalingGroup: %w", err)
		}

		if err := asg.ReduceCapacity(amount, drainer); err != nil {
			return newRuntimeError("failed to reduce the cluster capacity: %w", err)
		}
	} else {
		sfr, err := capacity.NewSpotFleetRequest(id, ec2.New(sess))
		if err != nil {
			return newRuntimeError("failed to initialize a SpotFleetRequest: %w", err)
		}

		sqsSvc := sqs.New(sess)
		queueURL, queueArn, err := putSQSQueue(sqsSvc, queueNameForInterruptionWarnings)
		if err != nil {
			return newRuntimeError("failed to create a queue for interruption warnings: %w", err)
		}

		eventsSvc := eventbridge.New(sess)
		targetID := "sqs"
		if err := putEventRule(eventsSvc, sqsSvc, ruleNameForInterruptionWarnings, targetID, queueURL, queueArn); err != nil {
			return newRuntimeError("failed to create an event rule for interruption warnings: %w", err)
		}

		if err = sfr.ReduceCapacity(amount, drainer, capacity.NewSQSQueuePoller(queueURL, sqsSvc)); err != nil {
			return newRuntimeError("failed to reduce the cluster capacity: %w", err)
		}

		if err := deleteEventRule(eventsSvc, ruleNameForInterruptionWarnings, targetID); err != nil {
			return newRuntimeError("failed to delete the event rule \"%s\": %w", ruleNameForInterruptionWarnings, err)
		}
		if err := deleteSQSQueue(sqsSvc, queueURL); err != nil {
			return newRuntimeError("failed to delete the SQS queue \"%s\": %w", queueNameForInterruptionWarnings, err)
		}
	}

	return nil
}

func putSQSQueue(svc *sqs.SQS, name string) (string, string, error) {
	queue, err := svc.CreateQueue(&sqs.CreateQueueInput{
		QueueName: aws.String(name),
	})
	if err != nil {
		return "", "", xerrors.Errorf("failed to create the SQS queue \"%s\": %w", name, err)
	}

	attrs, err := svc.GetQueueAttributes(&sqs.GetQueueAttributesInput{
		AttributeNames: []*string{
			aws.String("QueueArn"),
		},
		QueueUrl: queue.QueueUrl,
	})
	if err != nil {
		return "", "", xerrors.Errorf("failed to get queue attributes of the queue \"%s\": %w", name, err)
	}

	return *queue.QueueUrl, *attrs.Attributes["QueueArn"], nil
}

func deleteSQSQueue(svc *sqs.SQS, queueURL string) error {
	_, err := svc.DeleteQueue(&sqs.DeleteQueueInput{
		QueueUrl: aws.String(queueURL),
	})
	return err
}

func putEventRule(eventsSvc *eventbridge.EventBridge, sqsSvc *sqs.SQS, ruleName, targetID, queueURL, queueArn string) error {
	rule, err := eventsSvc.PutRule(&eventbridge.PutRuleInput{
		EventPattern: aws.String("{\"detail-type\":[\"EC2 Spot Instance Interruption Warning\"],\"source\":[\"aws.ec2\"]}"),
		Name:         aws.String(ruleName),
	})
	if err != nil {
		return xerrors.Errorf("failed to create a rule for interruption warnings: %w", err)
	}

	_, err = sqsSvc.SetQueueAttributes(&sqs.SetQueueAttributesInput{
		Attributes: map[string]*string{
			"Policy": aws.String(fmt.Sprintf(`{
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
		    }`, queueArn, *rule.RuleArn)),
		},
		QueueUrl: aws.String(queueURL),
	})
	if err != nil {
		return xerrors.Errorf("failed to update the queue access policy for interruption warnings: %w", err)
	}

	_, err = eventsSvc.PutTargets(&eventbridge.PutTargetsInput{
		Rule: aws.String(ruleName),
		Targets: []*eventbridge.Target{
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

func deleteEventRule(svc *eventbridge.EventBridge, ruleName, targetID string) error {
	_, err := svc.RemoveTargets(&eventbridge.RemoveTargetsInput{
		Ids:  []*string{aws.String(targetID)},
		Rule: aws.String(ruleName),
	})
	if err != nil {
		return xerrors.Errorf("failed to remove targets of the rule \"%s\": %w", ruleName, err)
	}

	_, err = svc.DeleteRule(&eventbridge.DeleteRuleInput{
		Force: aws.Bool(true),
		Name:  aws.String(ruleName),
	})
	return err
}
