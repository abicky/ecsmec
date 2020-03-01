package capacity

import (
	"fmt"
	"log"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/ecs"
	"github.com/aws/aws-sdk-go/service/ecs/ecsiface"
	"golang.org/x/xerrors"

	"github.com/abicky/ecsmec/internal/const/ecsconst"
	"github.com/abicky/ecsmec/internal/sliceutil"
)

type Drainer interface {
	Drain([]string) error
}

type drainer struct {
	cluster   string
	batchSize int64
	ecsSvc    ecsiface.ECSAPI
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
	params := &ecs.ListContainerInstancesInput{
		Cluster:    aws.String(d.cluster),
		Filter:     aws.String(fmt.Sprintf("ec2InstanceId in [%s]", strings.Join(instanceIDs, ","))),
		MaxResults: aws.Int64(d.batchSize),
	}

	var pageErr error
	processedCount := 0
	err := d.ecsSvc.ListContainerInstancesPages(params, func(page *ecs.ListContainerInstancesOutput, lastPage bool) bool {
		processedCount += len(page.ContainerInstanceArns)
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

		fmt.Printf("Drain the following container instances in the cluster \"%s\":\n", d.cluster)
		for _, i := range resp.ContainerInstances {
			arn := *i.ContainerInstanceArn
			fmt.Printf("\t%s (%s)\n", arn[strings.LastIndex(arn, "/")+1:], *i.Ec2InstanceId)
		}
		fmt.Printf("\nPress ENTER to continue ")
		fmt.Scanln()

		if err := d.drainContainerInstances(page.ContainerInstanceArns); err != nil {
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
	if processedCount == 0 {
		return xerrors.Errorf("no target instances exist in the cluster \"%s\"", d.cluster)
	}
	if processedCount != len(instanceIDs) {
		return xerrors.Errorf("%d instances should be drained but only %d instances was drained", len(instanceIDs), processedCount)
	}

	return nil
}

func (d *drainer) drainContainerInstances(arns []*string) error {
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
