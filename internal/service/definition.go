package service

import (
	"strings"

	"dario.cat/mergo"
	"github.com/aws/aws-sdk-go/service/ecs"
)

type Definition ecs.CreateServiceInput

func NewDefinitionFromExistingService(s *ecs.Service) *Definition {
	propagateTags := s.PropagateTags
	if propagateTags != nil && *propagateTags == "NONE" {
		propagateTags = nil
	}

	// Delete RoleArn to avoid "InvalidParameterException: You cannot specify an IAM role for services that require
	// a service linked role." if it is the service linked role.
	// According to the document, the name might have a suffix in the future.
	// cf. https://docs.aws.amazon.com/AmazonECS/latest/developerguide/using-service-linked-roles.html
	roleArn := s.RoleArn
	if roleArn != nil && strings.Contains(*roleArn, ":role/aws-service-role/ecs.amazonaws.com/AWSServiceRoleForECS") {
		roleArn = nil
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
		Role:                          roleArn,
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
