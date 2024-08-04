package service_test

import (
	"context"
	"io"
	"log"
	"os"
	"reflect"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"go.uber.org/mock/gomock"

	"github.com/abicky/ecsmec/internal/service"
	"github.com/abicky/ecsmec/internal/testing/servicemock"
	"github.com/abicky/ecsmec/internal/testing/testutil"
)

func TestMain(m *testing.M) {
	log.SetOutput(io.Discard)
	os.Exit(m.Run())
}

func expectCopy(
	t *testing.T,
	ctx context.Context,
	ecsMock *servicemock.MockECSAPI,
	cluster, srcServiceName, dstServiceName string,
	srcStrategy, dstStrategy []ecstypes.PlacementStrategy,
	desiredCount int32,
) *gomock.Call {
	t.Helper()

	return testutil.InOrder(
		ecsMock.EXPECT().DescribeServices(ctx, gomock.Any()).DoAndReturn(func(ctx context.Context, input *ecs.DescribeServicesInput, _ ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error) {
			if input.Services[0] != srcServiceName {
				t.Errorf("*input.Service[0] = %s; want %s", input.Services[0], srcServiceName)
			}
			return &ecs.DescribeServicesOutput{
				Services: []ecstypes.Service{
					{
						ClusterArn:        aws.String(cluster),
						ServiceName:       aws.String(srcServiceName),
						DesiredCount:      desiredCount,
						PlacementStrategy: srcStrategy,
						Status:            aws.String("ACTIVE"),
					},
				},
			}, nil
		}),

		ecsMock.EXPECT().CreateService(ctx, gomock.Any()).Do(func(_ context.Context, input *ecs.CreateServiceInput, _ ...func(*ecs.Options)) {
			if *input.ServiceName != dstServiceName {
				t.Errorf("*input.ServiceName = %s; want %s", *input.ServiceName, dstServiceName)
			}
			if *input.Cluster != cluster {
				t.Errorf("*input.Cluster = %s; want %s", *input.Cluster, cluster)
			}
			if *input.DesiredCount != desiredCount {
				t.Errorf("*input.DesiredCount = %d; want %d", *input.DesiredCount, desiredCount)
			}
			if !reflect.DeepEqual(input.PlacementStrategy, dstStrategy) {
				t.Errorf("input.PlacementStrategy = %#v; want %#v", input.PlacementStrategy, dstStrategy)
			}
		}),

		// For ecs.ServicesStableWaiter
		ecsMock.EXPECT().DescribeServices(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, input *ecs.DescribeServicesInput, _ ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error) {
			if input.Services[0] != dstServiceName {
				t.Errorf("*input.Service[0] = %s; want %s", input.Services[0], dstServiceName)
			}
			return &ecs.DescribeServicesOutput{
				Services: []ecstypes.Service{
					{
						Deployments:  make([]ecstypes.Deployment, 1),
						DesiredCount: desiredCount,
						RunningCount: desiredCount,
						Status:       aws.String("ACTIVE"),
					},
				},
			}, nil
		}),
	)
}

func expectStopAndDelete(
	t *testing.T,
	ctx context.Context,
	ecsMock *servicemock.MockECSAPI,
	cluster, serviceName string,
) *gomock.Call {
	t.Helper()

	runningTaskArns := []string{
		"arn:aws:ecs:ap-northeast-1:123456789:task/test/000bfe5f0fc14aeab304ebf1ba4c98ec",
		"arn:aws:ecs:ap-northeast-1:123456789:task/test/0230ff8ef0364f52b0461be9e074e4f9",
	}

	return testutil.InOrder(
		ecsMock.EXPECT().ListTasks(ctx, gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, params *ecs.ListTasksInput, _ ...func(*ecs.Options)) (*ecs.ListTasksOutput, error) {
				return &ecs.ListTasksOutput{
					TaskArns: runningTaskArns,
				}, nil
			}),

		ecsMock.EXPECT().UpdateService(ctx, gomock.Any()).Do(func(_ context.Context, input *ecs.UpdateServiceInput, _ ...func(*ecs.Options)) {
			if *input.Service != serviceName {
				t.Errorf("*input.ServiceName = %s; want %s", *input.Service, serviceName)
			}
			if *input.Cluster != cluster {
				t.Errorf("*input.Cluster = %s; want %s", *input.Cluster, cluster)
			}
			if *input.DesiredCount != 0 {
				t.Errorf("*input.DesiredCount = %d; want %d", *input.DesiredCount, 0)
			}
		}),

		// For ecs.TasksStoppedWaiter
		ecsMock.EXPECT().DescribeTasks(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, input *ecs.DescribeTasksInput, _ ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error) {
			if !reflect.DeepEqual(input.Tasks, runningTaskArns) {
				t.Errorf("input.Tasks = %#v; want %#v", input.Tasks, runningTaskArns)
			}
			return &ecs.DescribeTasksOutput{
				Tasks: []ecstypes.Task{
					{
						LastStatus: aws.String("STOPPED"),
					},
				},
			}, nil
		}),

		ecsMock.EXPECT().DeleteService(ctx, gomock.Any()).Do(func(_ context.Context, input *ecs.DeleteServiceInput, _ ...func(*ecs.Options)) {
			if *input.Service != serviceName {
				t.Errorf("*input.Service = %s; want %s", *input.Service, serviceName)
			}
		}),
	)
}

func TestService_Recreate(t *testing.T) {
	cluster := "default"
	serviceName := "test"

	tests := []struct {
		name      string
		overrides service.Definition
	}{
		{
			name:      "with overrides empty",
			overrides: service.Definition{},
		},
		{
			name: "with overriding placement strategy",
			overrides: service.Definition{
				PlacementStrategy: []ecstypes.PlacementStrategy{
					{
						Field: aws.String("CPU"),
						Type:  "binpack",
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			ctx := context.Background()

			ecsMock := servicemock.NewMockECSAPI(ctrl)
			tmpServiceName := serviceName + "-copied-by-ecsmec"

			gomock.InOrder(
				expectCopy(t, ctx, ecsMock, cluster, serviceName, tmpServiceName, nil, tt.overrides.PlacementStrategy, 1),
				expectStopAndDelete(t, ctx, ecsMock, cluster, serviceName),
				expectCopy(t, ctx, ecsMock, cluster, tmpServiceName, serviceName, tt.overrides.PlacementStrategy, tt.overrides.PlacementStrategy, 1),
				expectStopAndDelete(t, ctx, ecsMock, cluster, tmpServiceName),
			)

			s := service.NewService(ecsMock)
			if err := s.Recreate(ctx, cluster, serviceName, tt.overrides); err != nil {
				t.Errorf("err = %#v; want nil", err)
			}
		})
	}

	t.Run("with overriding service name", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		ctx := context.Background()

		ecsMock := servicemock.NewMockECSAPI(ctrl)

		newServiceName := "new-name"

		gomock.InOrder(
			expectCopy(t, ctx, ecsMock, cluster, serviceName, newServiceName, nil, nil, 1),
			expectStopAndDelete(t, ctx, ecsMock, cluster, serviceName),
		)

		s := service.NewService(ecsMock)
		if err := s.Recreate(ctx, cluster, serviceName, service.Definition{ServiceName: aws.String(newServiceName)}); err != nil {
			t.Errorf("err = %#v; want nil", err)
		}
	})

	exceptionTests := []struct {
		name     string
		services []ecstypes.Service
	}{
		{
			name:     "with unknown service name",
			services: []ecstypes.Service{},
		},
		{
			name: "with inactive service",
			services: []ecstypes.Service{
				{
					Status: aws.String("INACTIVE"),
				},
			},
		},
	}
	for _, tt := range exceptionTests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			ctx := context.Background()

			ecsMock := servicemock.NewMockECSAPI(ctrl)
			ecsMock.EXPECT().DescribeServices(ctx, gomock.Any()).Return(&ecs.DescribeServicesOutput{
				Services: tt.services,
			}, nil)

			s := service.NewService(ecsMock)
			if err := s.Recreate(ctx, cluster, serviceName, service.Definition{}); err == nil {
				t.Errorf("err = nil; want non-nil")
			}
		})
	}
}
