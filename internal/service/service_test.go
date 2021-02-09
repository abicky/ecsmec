package service_test

import (
	"io/ioutil"
	"log"
	"os"
	"reflect"
	"testing"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/ecs"
	"github.com/golang/mock/gomock"

	"github.com/abicky/ecsmec/internal/service"
	"github.com/abicky/ecsmec/internal/testing/mocks"
	"github.com/abicky/ecsmec/internal/testing/testutil"
)

func TestMain(m *testing.M) {
	log.SetOutput(ioutil.Discard)
	os.Exit(m.Run())
}

func expectCopy(
	t *testing.T,
	ecsMock *mocks.MockECSAPI,
	cluster, srcServiceName, dstServiceName string,
	srcStrategy, dstStrategy []*ecs.PlacementStrategy,
	desiredCount int64,
) *gomock.Call {
	t.Helper()

	return testutil.InOrder(
		ecsMock.EXPECT().DescribeServices(gomock.Any()).DoAndReturn(func(input *ecs.DescribeServicesInput) (*ecs.DescribeServicesOutput, error) {
			if *input.Services[0] != srcServiceName {
				t.Errorf("*input.Service[0] = %s; want %s", *input.Services[0], srcServiceName)
			}
			return &ecs.DescribeServicesOutput{
				Services: []*ecs.Service{
					{
						ClusterArn:        aws.String(cluster),
						ServiceName:       aws.String(srcServiceName),
						DesiredCount:      aws.Int64(desiredCount),
						PlacementStrategy: srcStrategy,
						Status:            aws.String("ACTIVE"),
					},
				},
			}, nil
		}),

		ecsMock.EXPECT().CreateService(gomock.Any()).Do(func(input *ecs.CreateServiceInput) {
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

		ecsMock.EXPECT().WaitUntilServicesStable(gomock.Any()),
	)
}

func expectStopAndDelete(
	t *testing.T,
	ecsMock *mocks.MockECSAPI,
	cluster, serviceName string,
) *gomock.Call {
	t.Helper()

	runningTaskArns := []string{
		"arn:aws:ecs:ap-northeast-1:123456789:task/test/000bfe5f0fc14aeab304ebf1ba4c98ec",
		"arn:aws:ecs:ap-northeast-1:123456789:task/test/0230ff8ef0364f52b0461be9e074e4f9",
	}

	return testutil.InOrder(
		ecsMock.EXPECT().ListTasksPages(gomock.Any(), gomock.Any()).
			DoAndReturn(func(params *ecs.ListTasksInput, fn func(*ecs.ListTasksOutput, bool) bool) error {
				fn(&ecs.ListTasksOutput{
					TaskArns: aws.StringSlice(runningTaskArns),
				}, true)
				return nil
			}),

		ecsMock.EXPECT().UpdateService(gomock.Any()).Do(func(input *ecs.UpdateServiceInput) {
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

		ecsMock.EXPECT().WaitUntilTasksStopped(gomock.Any()).Do(func(input *ecs.DescribeTasksInput) {
			want := aws.StringSlice(runningTaskArns)
			if !reflect.DeepEqual(input.Tasks, want) {
				t.Errorf("input.Tasks = %#v; want %#v", input.Tasks, want)
			}
		}),

		ecsMock.EXPECT().DeleteService(gomock.Any()).Do(func(input *ecs.DeleteServiceInput) {
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
				PlacementStrategy: []*ecs.PlacementStrategy{
					{
						Field: aws.String("CPU"),
						Type:  aws.String("binpack"),
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			ecsMock := mocks.NewMockECSAPI(ctrl)
			tmpServiceName := serviceName + "-copied-by-ecsmec"

			gomock.InOrder(
				expectCopy(t, ecsMock, cluster, serviceName, tmpServiceName, nil, tt.overrides.PlacementStrategy, 1),
				expectStopAndDelete(t, ecsMock, cluster, serviceName),
				expectCopy(t, ecsMock, cluster, tmpServiceName, serviceName, tt.overrides.PlacementStrategy, tt.overrides.PlacementStrategy, 1),
				expectStopAndDelete(t, ecsMock, cluster, tmpServiceName),
			)

			s := service.NewService(ecsMock)
			if err := s.Recreate(cluster, serviceName, tt.overrides); err != nil {
				t.Errorf("err = %#v; want nil", err)
			}
		})
	}

	t.Run("with overriding service name", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		ecsMock := mocks.NewMockECSAPI(ctrl)

		newServiceName := "new-name"

		gomock.InOrder(
			expectCopy(t, ecsMock, cluster, serviceName, newServiceName, nil, nil, 1),
			expectStopAndDelete(t, ecsMock, cluster, serviceName),
		)

		s := service.NewService(ecsMock)
		if err := s.Recreate(cluster, serviceName, service.Definition{ServiceName: aws.String(newServiceName)}); err != nil {
			t.Errorf("err = %#v; want nil", err)
		}
	})

	exceptionTests := []struct {
		name     string
		services []*ecs.Service
	}{
		{
			name:     "with unknown service name",
			services: []*ecs.Service{},
		},
		{
			name: "with unknown service name",
			services: []*ecs.Service{
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

			ecsMock := mocks.NewMockECSAPI(ctrl)
			ecsMock.EXPECT().DescribeServices(gomock.Any()).Return(&ecs.DescribeServicesOutput{
				Services: tt.services,
			}, nil)

			s := service.NewService(ecsMock)
			if err := s.Recreate(cluster, serviceName, service.Definition{}); err == nil {
				t.Errorf("err = nil; want non-nil")
			}
		})
	}
}
