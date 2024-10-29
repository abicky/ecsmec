package service

import (
	"strings"

	"dario.cat/mergo"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

type Definition ecs.CreateServiceInput

func NewDefinitionFromExistingService(s ecstypes.Service) *Definition {
	propagateTags := s.PropagateTags
	if propagateTags == ecstypes.PropagateTagsNone {
		propagateTags = ""
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
		DesiredCount:                  aws.Int32(s.DesiredCount),
		EnableECSManagedTags:          s.EnableECSManagedTags,
		EnableExecuteCommand:          s.EnableExecuteCommand,
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
		ServiceConnectConfiguration:   s.Deployments[0].ServiceConnectConfiguration,
		ServiceRegistries:             s.ServiceRegistries,
		Tags:                          s.Tags,
		TaskDefinition:                s.TaskDefinition,
		VolumeConfigurations:          s.Deployments[0].VolumeConfigurations,
	}
}

func (d *Definition) merge(other *Definition) error {
	return mergo.Merge(d, *other, mergo.WithOverride)
}

func (d *Definition) buildCreateServiceInput() *ecs.CreateServiceInput {
	return (*ecs.CreateServiceInput)(d)
}
