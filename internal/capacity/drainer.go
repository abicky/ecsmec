package capacity

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"golang.org/x/xerrors"

	"github.com/abicky/ecsmec/internal/const/ecsconst"
)

type Drainer interface {
	Drain(context.Context, []string) error
	ProcessInterruptions(context.Context, []sqstypes.Message) ([]sqstypes.DeleteMessageBatchRequestEntry, error)
}

type drainer struct {
	cluster   string
	batchSize int32
	ecsSvc    ECSAPI
}

// cf. https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/spot-interruptions.html
type interruptionWarning struct {
	Detail interruptionWarningDetail `json:"detail"`
}

type interruptionWarningDetail struct {
	InstanceID string `json:"instance-id"`
}

func NewDrainer(cluster string, batchSize int32, ecsSvc ECSAPI) (Drainer, error) {
	if batchSize > ecsconst.MaxListableContainerInstances {
		return nil, xerrors.Errorf("batchSize greater than %d is not supported", ecsconst.MaxListableContainerInstances)
	}
	return &drainer{
		cluster:   cluster,
		batchSize: batchSize,
		ecsSvc:    ecsSvc,
	}, nil
}

func (d *drainer) Drain(ctx context.Context, instanceIDs []string) error {
	processedCount := 0
	err := d.processContainerInstances(ctx, instanceIDs, func(instances []ecstypes.ContainerInstance) error {
		processedCount += len(instances)

		arns := make([]*string, len(instances))
		fmt.Printf("Drain the following container instances in the cluster \"%s\":\n", d.cluster)
		for i, instance := range instances {
			arns[i] = instance.ContainerInstanceArn
			fmt.Printf("\t%s (%s)\n", getContainerInstanceID(*instance.ContainerInstanceArn), *instance.Ec2InstanceId)
		}
		fmt.Printf("\nPress ENTER to continue ")
		fmt.Scanln()

		return d.drainContainerInstances(ctx, arns, true)
	})
	if err != nil {
		return xerrors.Errorf("failed to drain container instances: %w", err)
	}
	if processedCount == 0 {
		return xerrors.Errorf("no target instances exist in the cluster \"%s\"", d.cluster)
	}
	if processedCount != len(instanceIDs) {
		return xerrors.Errorf("%d instances should be drained but only %d instances was drained", len(instanceIDs), processedCount)
	}

	return nil
}

func (d *drainer) ProcessInterruptions(ctx context.Context, messages []sqstypes.Message) ([]sqstypes.DeleteMessageBatchRequestEntry, error) {
	if len(messages) == 0 {
		return nil, nil
	}

	instanceIDs := make([]string, len(messages))
	instanceIDToReceiptHandle := make(map[string]*string, len(messages))
	for i, m := range messages {
		var w interruptionWarning
		if err := json.Unmarshal([]byte(*m.Body), &w); err != nil {
			return nil, xerrors.Errorf("failed to parse the message: %s: %w", *m.Body, err)
		}
		instanceIDs[i] = w.Detail.InstanceID
		instanceIDToReceiptHandle[w.Detail.InstanceID] = m.ReceiptHandle
	}

	entries := make([]sqstypes.DeleteMessageBatchRequestEntry, 0)
	err := d.processContainerInstances(ctx, instanceIDs, func(instances []ecstypes.ContainerInstance) error {
		arns := make([]*string, len(instances))
		log.Printf("Drain the following container instances in the cluster \"%s\":\n", d.cluster)
		for i, instance := range instances {
			arns[i] = instance.ContainerInstanceArn
			log.Printf("\t%s (%s)\n", getContainerInstanceID(*instance.ContainerInstanceArn), *instance.Ec2InstanceId)
		}

		if err := d.drainContainerInstances(ctx, arns, false); err != nil {
			return xerrors.Errorf("failed to drain container instances: %w", err)
		}

		for _, instance := range instances {
			entries = append(entries, sqstypes.DeleteMessageBatchRequestEntry{
				Id:            instance.Ec2InstanceId,
				ReceiptHandle: instanceIDToReceiptHandle[*instance.Ec2InstanceId],
			})
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return entries, nil
}

func (d *drainer) drainContainerInstances(ctx context.Context, arns []*string, wait bool) error {
	allTaskArns := make([]string, 0)
	allServiceNames := make([]string, 0)
	for _, arn := range arns {
		params := &ecs.ListTasksInput{
			Cluster:           aws.String(d.cluster),
			ContainerInstance: arn,
		}

		paginator := ecs.NewListTasksPaginator(d.ecsSvc, params)
		for paginator.HasMorePages() {
			page, err := paginator.NextPage(ctx)
			if err != nil {
				return xerrors.Errorf("failed to list tasks: %w", err)
			}
			if len(page.TaskArns) == 0 {
				break
			}

			resp, err := d.ecsSvc.DescribeTasks(ctx, &ecs.DescribeTasksInput{
				Cluster: aws.String(d.cluster),
				Tasks:   page.TaskArns,
			})
			if err != nil {
				return xerrors.Errorf("failed to describe tasks: %w", err)
			}

			for _, t := range resp.Tasks {
				// The task group name of a task starting with "service:" means the task belongs to a service,
				// because other tasks can't have such a task group name due to the error "Invalid namespace for group".
				if strings.HasPrefix(*t.Group, "service:") {
					// Remove "service:" prefix
					allServiceNames = append(allServiceNames, (*t.Group)[8:])
				} else {
					// Stop tasks manually because tasks that don't belong to a service won't stop
					// even after their cluster instance's status becomes "DRAINING"
					log.Printf("Stop the task \"%s\"\n", *t.TaskArn)
					_, err := d.ecsSvc.StopTask(ctx, &ecs.StopTaskInput{
						Cluster: t.ClusterArn,
						Reason:  aws.String("Task stopped by ecsmec"),
						Task:    t.TaskArn,
					})
					if err != nil {
						return xerrors.Errorf("failed to stop the task: %w", err)
					}
				}
			}

			allTaskArns = append(allTaskArns, page.TaskArns...)
		}
	}

	for arns := range slices.Chunk(arns, ecsconst.MaxUpdatableContainerInstancesState) {
		_, err := d.ecsSvc.UpdateContainerInstancesState(ctx, &ecs.UpdateContainerInstancesStateInput{
			Cluster:            aws.String(d.cluster),
			ContainerInstances: aws.ToStringSlice(arns),
			Status:             "DRAINING",
		})
		if err != nil {
			return xerrors.Errorf("failed to update the container instances' state: %w", err)
		}
	}

	if !wait {
		return nil
	}

	log.Printf("Wait for all the tasks in the cluster \"%s\" to stop\n", d.cluster)
	tasksStoppedWaiter := ecs.NewTasksStoppedWaiter(d.ecsSvc, func(o *ecs.TasksStoppedWaiterOptions) {
		o.MaxDelay = 6 * time.Second
	})
	for arns := range slices.Chunk(allTaskArns, ecsconst.MaxDescribableTasks) {
		err := tasksStoppedWaiter.Wait(ctx, &ecs.DescribeTasksInput{
			Cluster: aws.String(d.cluster),
			Tasks:   arns,
		}, 10*time.Minute)
		if err != nil {
			return xerrors.Errorf("failed to wait for tasks to stop: %w", err)
		}
	}

	log.Printf("Wait for all the services in the cluster \"%s\" to become stable\n", d.cluster)
	servicesStableWaiter := ecs.NewServicesStableWaiter(d.ecsSvc, func(o *ecs.ServicesStableWaiterOptions) {
		o.MaxDelay = 15 * time.Second
	})
	for names := range slices.Chunk(allServiceNames, ecsconst.MaxDescribableServices) {
		err := servicesStableWaiter.Wait(ctx, &ecs.DescribeServicesInput{
			Cluster:  aws.String(d.cluster),
			Services: names,
		}, 10*time.Minute)
		if err != nil {
			return xerrors.Errorf("failed to wait for the services to become stable: %w", err)
		}
	}

	return nil
}

func (d *drainer) processContainerInstances(ctx context.Context, instanceIDs []string, callback func([]ecstypes.ContainerInstance) error) error {
	params := &ecs.ListContainerInstancesInput{
		Cluster:    aws.String(d.cluster),
		Filter:     aws.String(fmt.Sprintf("ec2InstanceId in [%s]", strings.Join(instanceIDs, ","))),
		MaxResults: aws.Int32(d.batchSize),
	}

	paginator := ecs.NewListContainerInstancesPaginator(d.ecsSvc, params)
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return xerrors.Errorf("failed to list container instances: %w", err)
		}
		if len(page.ContainerInstanceArns) == 0 {
			break
		}

		resp, err := d.ecsSvc.DescribeContainerInstances(ctx, &ecs.DescribeContainerInstancesInput{
			Cluster:            aws.String(d.cluster),
			ContainerInstances: page.ContainerInstanceArns,
		})
		if err != nil {
			return xerrors.Errorf("failed to describe container instances: %w", err)
		}

		if err := callback(resp.ContainerInstances); err != nil {
			return xerrors.Errorf("failed to list container instances: %w", err)
		}
	}

	return nil
}

func getContainerInstanceID(arn string) string {
	return arn[strings.LastIndex(arn, "/")+1:]
}
