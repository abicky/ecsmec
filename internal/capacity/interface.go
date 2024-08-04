package capacity

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

type AutoScalingAPI interface {
	CreateOrUpdateTags(context.Context, *autoscaling.CreateOrUpdateTagsInput, ...func(*autoscaling.Options)) (*autoscaling.CreateOrUpdateTagsOutput, error)
	DeleteTags(context.Context, *autoscaling.DeleteTagsInput, ...func(*autoscaling.Options)) (*autoscaling.DeleteTagsOutput, error)
	DescribeAutoScalingGroups(context.Context, *autoscaling.DescribeAutoScalingGroupsInput, ...func(*autoscaling.Options)) (*autoscaling.DescribeAutoScalingGroupsOutput, error)
	DetachInstances(context.Context, *autoscaling.DetachInstancesInput, ...func(*autoscaling.Options)) (*autoscaling.DetachInstancesOutput, error)
	UpdateAutoScalingGroup(context.Context, *autoscaling.UpdateAutoScalingGroupInput, ...func(*autoscaling.Options)) (*autoscaling.UpdateAutoScalingGroupOutput, error)
}

type EC2API interface {
	DescribeInstances(context.Context, *ec2.DescribeInstancesInput, ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
	DescribeSpotFleetInstances(context.Context, *ec2.DescribeSpotFleetInstancesInput, ...func(*ec2.Options)) (*ec2.DescribeSpotFleetInstancesOutput, error)
	DescribeSpotFleetRequests(context.Context, *ec2.DescribeSpotFleetRequestsInput, ...func(*ec2.Options)) (*ec2.DescribeSpotFleetRequestsOutput, error)
	ModifySpotFleetRequest(context.Context, *ec2.ModifySpotFleetRequestInput, ...func(*ec2.Options)) (*ec2.ModifySpotFleetRequestOutput, error)
	TerminateInstances(context.Context, *ec2.TerminateInstancesInput, ...func(*ec2.Options)) (*ec2.TerminateInstancesOutput, error)
}

type ECSAPI interface {
	DescribeContainerInstances(context.Context, *ecs.DescribeContainerInstancesInput, ...func(*ecs.Options)) (*ecs.DescribeContainerInstancesOutput, error)
	DescribeServices(context.Context, *ecs.DescribeServicesInput, ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error)
	DescribeTasks(context.Context, *ecs.DescribeTasksInput, ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error)
	ListContainerInstances(context.Context, *ecs.ListContainerInstancesInput, ...func(*ecs.Options)) (*ecs.ListContainerInstancesOutput, error)
	ListTasks(context.Context, *ecs.ListTasksInput, ...func(*ecs.Options)) (*ecs.ListTasksOutput, error)
	StopTask(context.Context, *ecs.StopTaskInput, ...func(*ecs.Options)) (*ecs.StopTaskOutput, error)
	UpdateContainerInstancesState(context.Context, *ecs.UpdateContainerInstancesStateInput, ...func(*ecs.Options)) (*ecs.UpdateContainerInstancesStateOutput, error)
}

type SQSAPI interface {
	DeleteMessageBatch(context.Context, *sqs.DeleteMessageBatchInput, ...func(*sqs.Options)) (*sqs.DeleteMessageBatchOutput, error)
	ReceiveMessage(context.Context, *sqs.ReceiveMessageInput, ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
}
