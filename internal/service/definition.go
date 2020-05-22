package service

import (
	"github.com/aws/aws-sdk-go/service/ecs"
	"github.com/imdario/mergo"
)

type Definition ecs.CreateServiceInput

func NewDefinitionFromExistingService(s *ecs.Service) *Definition {
	propagateTags := s.PropagateTags
	if propagateTags != nil && *propagateTags == "NONE" {
		propagateTags = nil
	}

	return &Definition{
		CapacityProviderStrategy:      s.CapacityProviderStrategy,
		Cluster:                       s.ClusterArn,
		DeploymentConfiguration:       s.DeploymentConfiguration,
		DeploymentController:          s.DeploymentController,
		DesiredCount:                  s.DesiredCount,
		EnableECSManagedTags:          s.EnableECSManagedTags,
		HealthCheckGracePeriodSeconds: s.HealthCheckGracePeriodSeconds,
		LaunchType:                    s.LaunchType,
		LoadBalancers:                 s.LoadBalancers,
		NetworkConfiguration:          s.NetworkConfiguration,
		PlacementConstraints:          s.PlacementConstraints,
		PlacementStrategy:             s.PlacementStrategy,
		PlatformVersion:               s.PlatformVersion,
		PropagateTags:                 propagateTags,
		Role:                          s.RoleArn,
		SchedulingStrategy:            s.SchedulingStrategy,
		ServiceName:                   s.ServiceName,
		ServiceRegistries:             s.ServiceRegistries,
		Tags:                          s.Tags,
		TaskDefinition:                s.TaskDefinition,
	}
}

func (d *Definition) merge(other *Definition) error {
	return mergo.Merge(d, *other, mergo.WithOverride)
}

func (d *Definition) buildCreateServiceInput() *ecs.CreateServiceInput {
	return (*ecs.CreateServiceInput)(d)
}
