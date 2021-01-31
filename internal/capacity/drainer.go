package capacity

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/ecs"
	"github.com/aws/aws-sdk-go/service/ecs/ecsiface"
	"github.com/aws/aws-sdk-go/service/sqs"
	"golang.org/x/xerrors"

	"github.com/abicky/ecsmec/internal/const/ecsconst"
	"github.com/abicky/ecsmec/internal/sliceutil"
)

type Drainer interface {
	Drain([]string) error
	ProcessInterruptions([]*sqs.Message) ([]*sqs.DeleteMessageBatchRequestEntry, error)
}

type drainer struct {
	cluster   string
	batchSize int64
	ecsSvc    ecsiface.ECSAPI
}

// cf. https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/spot-interruptions.html
type interruptionWarning struct {
	Detail interruptionWarningDetail `json:"detail"`
}

type interruptionWarningDetail struct {
	InstanceID string `json:"instance-id"`
}

func NewDrainer(cluster string, batchSize int64, ecsSvc ecsiface.ECSAPI) (Drainer, error) {
	if batchSize > ecsconst.MaxListableContainerInstances {
		return nil, xerrors.Errorf("batchSize greater than %d is not supported", ecsconst.MaxListableContainerInstances)
	}
	return &drainer{
		cluster:   cluster,
		batchSize: batchSize,
		ecsSvc:    ecsSvc,
	}, nil
}

func (d *drainer) Drain(instanceIDs []string) error {
	processedCount := 0
	err := d.processContainerInstances(instanceIDs, func(instances []*ecs.ContainerInstance) error {
		processedCount += len(instances)

		arns := make([]*string, len(instances))
		fmt.Printf("Drain the following container instances in the cluster \"%s\":\n", d.cluster)
		for i, instance := range instances {
			arns[i] = instance.ContainerInstanceArn
			fmt.Printf("\t%s (%s)\n", getContainerInstanceID(*instance.ContainerInstanceArn), *instance.Ec2InstanceId)
		}
		fmt.Printf("\nPress ENTER to continue ")
		fmt.Scanln()

		return d.drainContainerInstances(arns, true)
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

func (d *drainer) ProcessInterruptions(messages []*sqs.Message) ([]*sqs.DeleteMessageBatchRequestEntry, error) {
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

	entries := make([]*sqs.DeleteMessageBatchRequestEntry, 0)
	err := d.processContainerInstances(instanceIDs, func(instances []*ecs.ContainerInstance) error {
		arns := make([]*string, len(instances))
		log.Printf("Drain the following container instances in the cluster \"%s\":\n", d.cluster)
		for i, instance := range instances {
			arns[i] = instance.ContainerInstanceArn
			log.Printf("\t%s (%s)\n", getContainerInstanceID(*instance.ContainerInstanceArn), *instance.Ec2InstanceId)
		}

		if err := d.drainContainerInstances(arns, false); err != nil {
			return xerrors.Errorf("failed to drain container instances: %w", err)
		}

		for _, instance := range instances {
			entries = append(entries, &sqs.DeleteMessageBatchRequestEntry{
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

func (d *drainer) drainContainerInstances(arns []*string, wait bool) error {
	allTaskArns := make([]*string, 0)
	allServiceNames := make([]*string, 0)
	for _, arn := range arns {
		params := &ecs.ListTasksInput{
			Cluster:           aws.String(d.cluster),
			ContainerInstance: arn,
		}

		var pageErr error
		err := d.ecsSvc.ListTasksPages(params, func(page *ecs.ListTasksOutput, lastPage bool) bool {
			if len(page.TaskArns) == 0 {
				return false
			}

			resp, err := d.ecsSvc.DescribeTasks(&ecs.DescribeTasksInput{
				Cluster: aws.String(d.cluster),
				Tasks:   page.TaskArns,
			})
			if err != nil {
				pageErr = xerrors.Errorf("failed to describe tasks: %w", err)
				return false
			}

			for _, t := range resp.Tasks {
				// The task group name of a task starting with "service:" means the task belongs to a service,
				// because other tasks can't have such a task group name due to the error "Invalid namespace for group".
				if strings.HasPrefix(*t.Group, "service:") {
					// Remove "service:" prefix
					allServiceNames = append(allServiceNames, aws.String((*t.Group)[8:]))
				} else {
					// Stop tasks manually because tasks that don't belong to a service won't stop
					// even after their cluster instance's status becomes "DRAINING"
					log.Printf("Stop the task \"%s\"\n", *t.TaskArn)
					_, err = d.ecsSvc.StopTask(&ecs.StopTaskInput{
						Cluster: t.ClusterArn,
						Reason:  aws.String("Task stopped by ecsmec"),
						Task:    t.TaskArn,
					})
					if err != nil {
						pageErr = xerrors.Errorf("failed to stop the task: %w", err)
						return false
					}
				}
			}

			allTaskArns = append(allTaskArns, page.TaskArns...)
			return true
		})
		if err != nil {
			return xerrors.Errorf("failed to list tasks: %w", err)
		}
		if pageErr != nil {
			return xerrors.Errorf("failed to list tasks: %w", pageErr)
		}
	}

	for arns := range sliceutil.ChunkSlice(arns, ecsconst.MaxUpdatableContainerInstancesState) {
		_, err := d.ecsSvc.UpdateContainerInstancesState(&ecs.UpdateContainerInstancesStateInput{
			Cluster:            aws.String(d.cluster),
			ContainerInstances: arns,
			Status:             aws.String("DRAINING"),
		})
		if err != nil {
			return xerrors.Errorf("failed to update the container instances' state: %w", err)
		}
	}

	if !wait {
		return nil
	}

	log.Printf("Wait for all the tasks in the cluster \"%s\" to stop\n", d.cluster)
	for arns := range sliceutil.ChunkSlice(allTaskArns, ecsconst.MaxDescribableTasks) {
		err := d.ecsSvc.WaitUntilTasksStopped(&ecs.DescribeTasksInput{
			Cluster: aws.String(d.cluster),
			Tasks:   arns,
		})
		if err != nil {
			return xerrors.Errorf("failed to wait for the tasks to stop: %w", err)
		}
	}

	log.Printf("Wait for all the services in the cluster \"%s\" to become stable\n", d.cluster)
	for names := range sliceutil.ChunkSlice(allServiceNames, ecsconst.MaxDescribableServices) {
		err := d.ecsSvc.WaitUntilServicesStable(&ecs.DescribeServicesInput{
			Cluster:  aws.String(d.cluster),
			Services: names,
		})
		if err != nil {
			return xerrors.Errorf("failed to wait for the services to become stable: %w", err)
		}
	}

	return nil
}

func (d *drainer) processContainerInstances(instanceIDs []string, callback func([]*ecs.ContainerInstance) error) error {
	params := &ecs.ListContainerInstancesInput{
		Cluster:    aws.String(d.cluster),
		Filter:     aws.String(fmt.Sprintf("ec2InstanceId in [%s]", strings.Join(instanceIDs, ","))),
		MaxResults: aws.Int64(d.batchSize),
	}

	var pageErr error
	err := d.ecsSvc.ListContainerInstancesPages(params, func(page *ecs.ListContainerInstancesOutput, lastPage bool) bool {
		if len(page.ContainerInstanceArns) == 0 {
			return true
		}

		resp, err := d.ecsSvc.DescribeContainerInstances(&ecs.DescribeContainerInstancesInput{
			Cluster:            aws.String(d.cluster),
			ContainerInstances: page.ContainerInstanceArns,
		})
		if err != nil {
			pageErr = xerrors.Errorf("failed to describe container instances: %w", err)
			return false
		}

		if err := callback(resp.ContainerInstances); err != nil {
			pageErr = err
			return false
		}
		return true
	})
	if err != nil {
		return xerrors.Errorf("failed to list container instances: %w", err)
	}
	if pageErr != nil {
		return xerrors.Errorf("failed to list container instances: %w", pageErr)
	}

	return nil
}

func getContainerInstanceID(arn string) string {
	return arn[strings.LastIndex(arn, "/")+1:]
}
