package service

import (
	"context"
	"log"
	"slices"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"golang.org/x/xerrors"

	"github.com/abicky/ecsmec/internal/const/ecsconst"
)

type Service struct {
	ecsSvc ECSAPI
}

func NewService(ecsSvc ECSAPI) *Service {
	return &Service{
		ecsSvc,
	}
}

func (s *Service) Recreate(ctx context.Context, cluster string, serviceName string, overrides Definition) error {
	newServiceName := overrides.ServiceName
	tmpServiceName := serviceName + "-copied-by-ecsmec"
	if newServiceName == nil {
		overrides.ServiceName = aws.String(tmpServiceName)
	}

	if err := s.copy(ctx, cluster, serviceName, overrides); err != nil {
		return xerrors.Errorf("failed to copy the service \"%s\" to \"%s\": %w", serviceName, *overrides.ServiceName, err)
	}

	if err := s.stopAndDelete(ctx, cluster, serviceName); err != nil {
		return xerrors.Errorf("failed to stop and delete the service \"%s\": %w", serviceName, err)
	}

	if newServiceName == nil {
		if err := s.copy(ctx, cluster, tmpServiceName, Definition{ServiceName: aws.String(serviceName)}); err != nil {
			return xerrors.Errorf("failed to copy the service \"%s\" to \"%s\": %w", tmpServiceName, serviceName, err)
		}

		if err := s.stopAndDelete(ctx, cluster, tmpServiceName); err != nil {
			return xerrors.Errorf("failed to stop and delete the service \"%s\": %w", tmpServiceName, err)
		}
	}

	return nil
}

func (s *Service) copy(ctx context.Context, cluster string, serviceName string, overrides Definition) error {
	resp, err := s.ecsSvc.DescribeServices(ctx, &ecs.DescribeServicesInput{
		Cluster:  aws.String(cluster),
		Include:  []ecstypes.ServiceField{ecstypes.ServiceFieldTags},
		Services: []string{serviceName},
	})
	if err != nil {
		return xerrors.Errorf("failed to describe the service \"%s\": %w", serviceName, err)
	}
	if len(resp.Services) == 0 {
		return xerrors.Errorf("the service \"%s\" doesn't exist", serviceName)
	}
	if *resp.Services[0].Status != "ACTIVE" {
		return xerrors.Errorf("the service \"%s\" is not active", serviceName)
	}

	def := NewDefinitionFromExistingService(resp.Services[0])
	if err := def.merge(&overrides); err != nil {
		return xerrors.Errorf("failed to merge the overrides: %w", err)
	}

	config := def.buildCreateServiceInput()
	log.Printf("Create the following service and wait for it to become stable\n%#v\n", config)
	err = retryOnServiceCreationTempErr(func() error {
		return s.createAndWaitUntilStable(ctx, config)
	}, 60)
	if err != nil {
		return xerrors.Errorf("failed to create the service and wait for it to become stable: %w", err)
	}

	return nil
}

func (s *Service) createAndWaitUntilStable(ctx context.Context, config *ecs.CreateServiceInput) error {
	if _, err := s.ecsSvc.CreateService(ctx, config); err != nil {
		return xerrors.Errorf("failed to create the service \"%s\": %w", *config.ServiceName, err)
	}

	waiter := ecs.NewServicesStableWaiter(s.ecsSvc, func(o *ecs.ServicesStableWaiterOptions) {
		o.MaxDelay = 15 * time.Second
	})
	err := waiter.Wait(ctx, &ecs.DescribeServicesInput{
		Cluster:  config.Cluster,
		Services: []string{*config.ServiceName},
	}, 10*time.Minute)
	if err != nil {
		return xerrors.Errorf("failed to wait for the service \"%s\" to become stable: %w", *config.ServiceName, err)
	}

	return nil
}

func (s *Service) stopAndDelete(ctx context.Context, cluster string, serviceName string) error {
	log.Printf("Stop all the tasks of the service \"%s\" and wait for them to stop\n", serviceName)
	if err := s.stopAndWaitUntilStopped(ctx, cluster, serviceName); err != nil {
		return xerrors.Errorf("failed to stop all the tasks of the service and wait for them to stop: %w", err)
	}

	log.Printf("Delete the service \"%s\"\n", serviceName)
	if err := s.delete(ctx, cluster, serviceName); err != nil {
		return xerrors.Errorf("failed to delete the service: %w", err)
	}

	return nil
}

func (s *Service) stopAndWaitUntilStopped(ctx context.Context, cluster string, serviceName string) error {
	taskArns := make([]string, 0)
	params := &ecs.ListTasksInput{
		Cluster:       aws.String(cluster),
		DesiredStatus: ecstypes.DesiredStatusRunning,
		ServiceName:   aws.String(serviceName),
	}

	paginator := ecs.NewListTasksPaginator(s.ecsSvc, params)
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return xerrors.Errorf("failed to list tasks: %w", err)
		}
		taskArns = append(taskArns, page.TaskArns...)
	}

	_, err := s.ecsSvc.UpdateService(ctx, &ecs.UpdateServiceInput{
		Cluster:      aws.String(cluster),
		DesiredCount: aws.Int32(0),
		Service:      aws.String(serviceName),
	})
	if err != nil {
		return xerrors.Errorf("failed to update the desired count to 0: %w", err)
	}

	waiter := ecs.NewTasksStoppedWaiter(s.ecsSvc, func(o *ecs.TasksStoppedWaiterOptions) {
		o.MaxDelay = 6 * time.Second
	})
	for arns := range slices.Chunk(taskArns, ecsconst.MaxDescribableTasks) {
		err := waiter.Wait(ctx, &ecs.DescribeTasksInput{
			Cluster: aws.String(cluster),
			Tasks:   arns,
		}, 10*time.Minute)
		if err != nil {
			return xerrors.Errorf("failed to wait for tasks to stop: %w", err)
		}
	}

	return nil
}

func (s *Service) delete(ctx context.Context, cluster string, serviceName string) error {
	_, err := s.ecsSvc.DeleteService(ctx, &ecs.DeleteServiceInput{
		Cluster: aws.String(cluster),
		Service: aws.String(serviceName),
	})
	if err != nil {
		return xerrors.Errorf("failed to delete the service \"%s\": %w", serviceName, err)
	}

	return nil
}

func retryOnServiceCreationTempErr(fn func() error, tries int) error {
	var err error
	for i := range tries {
		if err = fn(); err == nil {
			break
		}

		var e *ecstypes.InvalidParameterException
		if !xerrors.As(err, &e) || e.ErrorMessage() != "Unable to Start a service that is still Draining." {
			break
		}

		if i < tries {
			log.Printf("Retry to create the service in 1s due to the error \"%s\"", e)
			time.Sleep(time.Second)
		}
	}

	return err
}
