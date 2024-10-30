package capacity

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"golang.org/x/xerrors"
)

type Cluster interface {
	Name() string
	WaitUntilContainerInstancesRegistered(context.Context, int, *time.Time) error
}

type cluster struct {
	name   string
	ecsSvc ECSAPI
}

func NewCluster(name string, ecsSvc ECSAPI) Cluster {
	return &cluster{
		name:   name,
		ecsSvc: ecsSvc,
	}
}

func (c *cluster) Name() string {
	return c.name
}

func (c *cluster) WaitUntilContainerInstancesRegistered(ctx context.Context, count int, registeredAt *time.Time) error {
	if count == 0 {
		return nil
	}

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	timeout := 5 * time.Minute
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	params := &ecs.ListContainerInstancesInput{
		Cluster: aws.String(c.name),
		Filter:  aws.String(fmt.Sprintf("registeredAt >= %s", registeredAt.UTC().Format(time.RFC3339))),
	}
	for {
		foundCount := 0
		paginator := ecs.NewListContainerInstancesPaginator(c.ecsSvc, params)
		for paginator.HasMorePages() {
			page, err := paginator.NextPage(ctx)
			if err != nil {
				return xerrors.Errorf("failed to list container instances: %w", err)
			}
			foundCount += len(page.ContainerInstanceArns)
		}
		if foundCount == count {
			return nil
		}

		select {
		case <-ticker.C:
			continue
		case <-timer.C:
			return xerrors.Errorf("%d container instances expect to be registered but only %d instances were registered within %v", count, foundCount, timeout)
		}
	}
}
