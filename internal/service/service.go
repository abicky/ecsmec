package service

import (
	"log"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/ecs"
	"github.com/aws/aws-sdk-go/service/ecs/ecsiface"
	"golang.org/x/xerrors"

	"github.com/abicky/ecsmec/internal/const/ecsconst"
	"github.com/abicky/ecsmec/internal/sliceutil"
)

type Service struct {
	ecsSvc ecsiface.ECSAPI
}

func NewService(ecsSvc ecsiface.ECSAPI) *Service {
	return &Service{
		ecsSvc,
	}
}

func (s *Service) Recreate(cluster string, serviceName string, overrides Definition) error {
	newServiceName := overrides.ServiceName
	tmpServiceName := serviceName + "-copied-by-ecsmec"
	if newServiceName == nil {
		overrides.ServiceName = aws.String(tmpServiceName)
	}

	if err := s.copy(cluster, serviceName, overrides); err != nil {
		return xerrors.Errorf("failed to copy the service \"%s\" to \"%s\": %w", serviceName, *overrides.ServiceName, err)
	}

	if err := s.stopAndDelete(cluster, serviceName); err != nil {
		return xerrors.Errorf("failed to stop and delete the service \"%s\": %w", serviceName, err)
	}

	if newServiceName == nil {
		if err := s.copy(cluster, tmpServiceName, Definition{ServiceName: aws.String(serviceName)}); err != nil {
			return xerrors.Errorf("failed to copy the service \"%s\" to \"%s\": %w", tmpServiceName, serviceName, err)
		}

		if err := s.stopAndDelete(cluster, tmpServiceName); err != nil {
			return xerrors.Errorf("failed to stop and delete the service \"%s\": %w", tmpServiceName, err)
		}
	}

	return nil
}

func (s *Service) copy(cluster string, serviceName string, overrides Definition) error {
	resp, err := s.ecsSvc.DescribeServices(&ecs.DescribeServicesInput{
		Cluster:  aws.String(cluster),
		Include:  []*string{aws.String("TAGS")},
		Services: []*string{aws.String(serviceName)},
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
		return s.createAndWaitUntilStable(config)
	}, 60)
	if err != nil {
		return xerrors.Errorf("failed to create the service and wait for it to become stable: %w", err)
	}

	return nil
}

func (s *Service) createAndWaitUntilStable(config *ecs.CreateServiceInput) error {
	_, err := s.ecsSvc.CreateService(config)
	if err != nil {
		return xerrors.Errorf("failed to create the service \"%s\": %w", *config.ServiceName, err)
	}

	err = s.ecsSvc.WaitUntilServicesStable(&ecs.DescribeServicesInput{
		Cluster:  config.Cluster,
		Services: []*string{config.ServiceName},
	})
	if err != nil {
		return xerrors.Errorf("failed to wait for the service \"%s\" to become stable: %w", *config.ServiceName, err)
	}

	return nil
}

func (s *Service) stopAndDelete(cluster string, serviceName string) error {
	log.Printf("Stop all the tasks of the service \"%s\" and wait for them to stop\n", serviceName)
	if err := s.stopAndWaitUntilStopped(cluster, serviceName); err != nil {
		return xerrors.Errorf("failed to stop all the tasks of the service and wait for them to stop: %w", err)
	}

	log.Printf("Delete the service \"%s\"\n", serviceName)
	if err := s.delete(cluster, serviceName); err != nil {
		return xerrors.Errorf("failed to delete the service: %w", err)
	}

	return nil
}

func (s *Service) stopAndWaitUntilStopped(cluster string, serviceName string) error {
	taskArns := make([]*string, 0)
	params := &ecs.ListTasksInput{
		Cluster:       aws.String(cluster),
		DesiredStatus: aws.String("RUNNING"),
		ServiceName:   aws.String(serviceName),
	}

	err := s.ecsSvc.ListTasksPages(params, func(page *ecs.ListTasksOutput, lastPage bool) bool {
		taskArns = append(taskArns, page.TaskArns...)
		return true
	})
	if err != nil {
		return xerrors.Errorf("failed to list tasks: %w", err)
	}

	_, err = s.ecsSvc.UpdateService(&ecs.UpdateServiceInput{
		Cluster:      aws.String(cluster),
		DesiredCount: aws.Int64(0),
		Service:      aws.String(serviceName),
	})
	if err != nil {
		return xerrors.Errorf("failed to update the desired count to 0: %w", err)
	}

	for arns := range sliceutil.ChunkSlice(taskArns, ecsconst.MaxDescribableTasks) {
		err = s.ecsSvc.WaitUntilTasksStopped(&ecs.DescribeTasksInput{
			Cluster: aws.String(cluster),
			Tasks:   arns,
		})
		if err != nil {
			return xerrors.Errorf("failed to wait for tasks to stop: %w", err)
		}
	}

	return nil
}

func (s *Service) delete(cluster string, serviceName string) error {
	_, err := s.ecsSvc.DeleteService(&ecs.DeleteServiceInput{
		Cluster: aws.String(cluster),
		Service: aws.String(serviceName),
	})
	if err != nil {
		return xerrors.Errorf("failed to delete the service \"%s\": %w", serviceName, err)
	}

	return nil
}

func retryOnServiceCreationTempErr(fn func() error, tries int) error {
	for i := 0; i < tries; i++ {
		if err := fn(); err != nil {
			var e *ecs.InvalidParameterException
			if xerrors.As(err, &e) && e.Message() == "Unable to Start a service that is still Draining." {
				time.Sleep(time.Second)
				continue
			}
			return err
		}

		break
	}

	return nil
}
