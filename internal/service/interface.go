package service

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/ecs"
)

type ECSAPI interface {
	CreateService(context.Context, *ecs.CreateServiceInput, ...func(*ecs.Options)) (*ecs.CreateServiceOutput, error)
	DeleteService(context.Context, *ecs.DeleteServiceInput, ...func(*ecs.Options)) (*ecs.DeleteServiceOutput, error)
	DescribeServices(context.Context, *ecs.DescribeServicesInput, ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error)
	DescribeTasks(context.Context, *ecs.DescribeTasksInput, ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error)
	ListTasks(context.Context, *ecs.ListTasksInput, ...func(*ecs.Options)) (*ecs.ListTasksOutput, error)
	UpdateService(context.Context, *ecs.UpdateServiceInput, ...func(*ecs.Options)) (*ecs.UpdateServiceOutput, error)
}
