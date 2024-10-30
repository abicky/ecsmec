package capacity_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/abicky/ecsmec/internal/testing/testutil"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"go.uber.org/mock/gomock"

	"github.com/abicky/ecsmec/internal/capacity"
	"github.com/abicky/ecsmec/internal/testing/capacitymock"
)

func TestSpotFleetRequest_TerminateAllInstances(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	spotFleetRequestID := "sfr-39d27795-73f7-4c2d-976f-3262e0c988af"

	instances := append(
		createInstances("ap-northeast-1a", 1),
		createInstances("ap-northeast-1c", 1)...,
	)

	activeInstances := make([]ec2types.ActiveInstance, len(instances))
	for i, instance := range instances {
		activeInstances[i] = ec2types.ActiveInstance{
			InstanceId:            instance.InstanceId,
			SpotInstanceRequestId: aws.String(spotFleetRequestID),
		}
	}

	t.Run("with the state cancelled_running", func(t *testing.T) {
		ctx := context.Background()

		ec2Mock := capacitymock.NewMockEC2API(ctrl)
		drainerMock := capacitymock.NewMockDrainer(ctrl)

		gomock.InOrder(
			ec2Mock.EXPECT().DescribeSpotFleetRequests(ctx, gomock.Any()).Return(&ec2.DescribeSpotFleetRequestsOutput{
				SpotFleetRequestConfigs: []ec2types.SpotFleetRequestConfig{
					{
						SpotFleetRequestConfig: &ec2types.SpotFleetRequestConfigData{
							Type: "maintain",
						},
						SpotFleetRequestId:    aws.String(spotFleetRequestID),
						SpotFleetRequestState: "cancelled_running",
					},
				},
			}, nil),

			ec2Mock.EXPECT().DescribeSpotFleetInstances(ctx, gomock.Any()).Return(&ec2.DescribeSpotFleetInstancesOutput{
				ActiveInstances:    activeInstances,
				SpotFleetRequestId: aws.String(spotFleetRequestID),
			}, nil),

			drainerMock.EXPECT().Drain(ctx, gomock.Len(len(instances))),

			ec2Mock.EXPECT().TerminateInstances(ctx, gomock.Any()).Do(func(_ context.Context, input *ec2.TerminateInstancesInput, _ ...func(*ec2.Options)) {
				want := make([]string, len(instances))
				for i, instance := range instances {
					want[i] = *instance.InstanceId
				}
				got := input.InstanceIds
				if !reflect.DeepEqual(got, want) {
					t.Errorf("aws.StringValueSlice(input.InstanceIds) = %#v; want %#v", got, want)
				}
			}),

			// For InstanceTerminatedWaiter
			ec2Mock.EXPECT().DescribeInstances(testutil.AnyContext(), gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, input *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
				instanceIds := make([]string, len(instances))
				for i, instance := range instances {
					instanceIds[i] = *instance.InstanceId
				}

				if !testutil.MatchSlice(input.InstanceIds, instanceIds) {
					t.Errorf("input.InstanceIds = %v; want %v", input.InstanceIds, instanceIds)
				}

				instances := make([]ec2types.Instance, len(instanceIds))
				for i, id := range instanceIds {
					instances[i] = ec2types.Instance{
						InstanceId: aws.String(id),
						State: &ec2types.InstanceState{
							Name: "terminated",
						},
					}
				}

				return &ec2.DescribeInstancesOutput{
					Reservations: []ec2types.Reservation{
						{
							Instances: instances,
						},
					},
				}, nil
			}),
		)

		sfr, err := capacity.NewSpotFleetRequest(spotFleetRequestID, ec2Mock)
		if err != nil {
			t.Fatal(err)
		}

		if err := sfr.TerminateAllInstances(ctx, drainerMock); err != nil {
			t.Errorf("err = %#v; want nil", err)
		}
	})

	t.Run("with the state active", func(t *testing.T) {
		ctx := context.Background()

		ec2Mock := capacitymock.NewMockEC2API(ctrl)
		drainerMock := capacitymock.NewMockDrainer(ctrl)

		gomock.InOrder(
			ec2Mock.EXPECT().DescribeSpotFleetRequests(ctx, gomock.Any()).Return(&ec2.DescribeSpotFleetRequestsOutput{
				SpotFleetRequestConfigs: []ec2types.SpotFleetRequestConfig{
					{
						SpotFleetRequestConfig: &ec2types.SpotFleetRequestConfigData{
							Type: "maintain",
						},
						SpotFleetRequestId:    aws.String(spotFleetRequestID),
						SpotFleetRequestState: "active",
					},
				},
			}, nil),

			ec2Mock.EXPECT().DescribeSpotFleetInstances(ctx, gomock.Any()).Return(&ec2.DescribeSpotFleetInstancesOutput{
				ActiveInstances:    activeInstances,
				SpotFleetRequestId: aws.String(spotFleetRequestID),
			}, nil),
		)

		sfr, err := capacity.NewSpotFleetRequest(spotFleetRequestID, ec2Mock)
		if err != nil {
			t.Fatal(err)
		}

		if err := sfr.TerminateAllInstances(ctx, drainerMock); err == nil {
			t.Errorf("err = nil; want non-nil")
		}
	})
}

func TestSpotFleetRequest_ReduceCapacity(t *testing.T) {
	tests := []struct {
		name                 string
		drainedInstanceCount int
		finalTargetCapacity  int32
		amount               int32
		config               *ec2types.SpotFleetRequestConfigData
	}{
		{
			name:                 "without weighted capacity in the launch template configs",
			drainedInstanceCount: 3,
			finalTargetCapacity:  7,
			amount:               3,
			config: &ec2types.SpotFleetRequestConfigData{
				LaunchTemplateConfigs: []ec2types.LaunchTemplateConfig{
					{},
				},
				TargetCapacity: aws.Int32(10),
			},
		},
		{
			name:                 "with weighted capacity in the launch template configs",
			drainedInstanceCount: 1,
			finalTargetCapacity:  7,
			amount:               3,
			config: &ec2types.SpotFleetRequestConfigData{
				LaunchTemplateConfigs: []ec2types.LaunchTemplateConfig{
					{
						Overrides: []ec2types.LaunchTemplateOverrides{
							{
								WeightedCapacity: aws.Float64(2),
							},
						},
					},
				},
				TargetCapacity: aws.Int32(10),
			},
		},
		{
			name:                 "without weighed capacity in the launch specifications",
			drainedInstanceCount: 3,
			finalTargetCapacity:  7,
			amount:               3,
			config: &ec2types.SpotFleetRequestConfigData{
				LaunchSpecifications: []ec2types.SpotFleetLaunchSpecification{
					{},
				},
				TargetCapacity: aws.Int32(10),
			},
		},
		{
			name:                 "with weighed capacity in the launch specifications",
			drainedInstanceCount: 1,
			finalTargetCapacity:  7,
			amount:               3,
			config: &ec2types.SpotFleetRequestConfigData{
				LaunchSpecifications: []ec2types.SpotFleetLaunchSpecification{
					{
						WeightedCapacity: aws.Float64(2),
					},
				},
				TargetCapacity: aws.Int32(10),
			},
		},
		{
			name:                 "when the amount is greater than the current target capacity",
			drainedInstanceCount: 1,
			finalTargetCapacity:  0,
			amount:               2,
			config: &ec2types.SpotFleetRequestConfigData{
				LaunchTemplateConfigs: []ec2types.LaunchTemplateConfig{
					{},
				},
				TargetCapacity: aws.Int32(1),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			ctx := context.Background()

			spotFleetRequestID := "sfr-39d27795-73f7-4c2d-976f-3262e0c988af"
			ec2Mock := capacitymock.NewMockEC2API(ctrl)
			drainerMock := capacitymock.NewMockDrainer(ctrl)
			pollerMock := capacitymock.NewMockPoller(ctrl)

			messages := make([]sqstypes.Message, tt.drainedInstanceCount)
			entries := make([]sqstypes.DeleteMessageBatchRequestEntry, tt.drainedInstanceCount)

			pollerMock.EXPECT().Poll(gomock.Any(), gomock.Any()).Do(func(ctx context.Context, fn func([]sqstypes.Message) ([]sqstypes.DeleteMessageBatchRequestEntry, error)) {
				fn(messages)
			})

			gomock.InOrder(
				ec2Mock.EXPECT().DescribeSpotFleetRequests(ctx, gomock.Any()).Return(&ec2.DescribeSpotFleetRequestsOutput{
					SpotFleetRequestConfigs: []ec2types.SpotFleetRequestConfig{
						{
							SpotFleetRequestId:     aws.String(spotFleetRequestID),
							SpotFleetRequestConfig: tt.config,
						},
					},
				}, nil),

				ec2Mock.EXPECT().ModifySpotFleetRequest(ctx, gomock.Any()).Do(func(_ context.Context, input *ec2.ModifySpotFleetRequestInput, _ ...func(*ec2.Options)) {
					if *input.TargetCapacity != tt.finalTargetCapacity {
						t.Errorf("*input.TargetCapacity = %d; want %d", *input.TargetCapacity, tt.finalTargetCapacity)
					}
				}),

				drainerMock.EXPECT().ProcessInterruptions(ctx, messages).Return(entries, nil),
			)

			sfr, err := capacity.NewSpotFleetRequest(spotFleetRequestID, ec2Mock)
			if err != nil {
				t.Fatal(err)
			}

			if err := sfr.ReduceCapacity(ctx, tt.amount, drainerMock, pollerMock); err != nil {
				t.Errorf("err = %#v; want nil", err)
			}
		})
	}

	t.Run("when the target capacity is already 0", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		ctx := context.Background()

		spotFleetRequestID := "sfr-39d27795-73f7-4c2d-976f-3262e0c988af"
		ec2Mock := capacitymock.NewMockEC2API(ctrl)
		drainerMock := capacitymock.NewMockDrainer(ctrl)
		pollerMock := capacitymock.NewMockPoller(ctrl)

		ec2Mock.EXPECT().DescribeSpotFleetRequests(ctx, gomock.Any()).Return(&ec2.DescribeSpotFleetRequestsOutput{
			SpotFleetRequestConfigs: []ec2types.SpotFleetRequestConfig{
				{
					SpotFleetRequestId: aws.String(spotFleetRequestID),
					SpotFleetRequestConfig: &ec2types.SpotFleetRequestConfigData{
						TargetCapacity: aws.Int32(0),
					},
				},
			},
		}, nil)

		sfr, err := capacity.NewSpotFleetRequest(spotFleetRequestID, ec2Mock)
		if err != nil {
			t.Fatal(err)
		}

		if err := sfr.ReduceCapacity(ctx, 1, drainerMock, pollerMock); err != nil {
			t.Errorf("err = %#v; want nil", err)
		}
	})

	exceptionTests := []struct {
		name   string
		config *ec2types.SpotFleetRequestConfigData
	}{
		{
			name: "with mixed weighted capacities and a launch template",
			config: &ec2types.SpotFleetRequestConfigData{
				LaunchTemplateConfigs: []ec2types.LaunchTemplateConfig{
					{
						Overrides: []ec2types.LaunchTemplateOverrides{
							{
								WeightedCapacity: aws.Float64(1),
							},
							{
								WeightedCapacity: aws.Float64(2),
							},
						},
					},
				},
				TargetCapacity: aws.Int32(1),
			},
		},
		{
			name: "with mixed weighted capacities and without a launch template",
			config: &ec2types.SpotFleetRequestConfigData{
				LaunchSpecifications: []ec2types.SpotFleetLaunchSpecification{
					{
						WeightedCapacity: aws.Float64(1),
					},
					{
						WeightedCapacity: aws.Float64(2),
					},
				},
				TargetCapacity: aws.Int32(1),
			},
		},
		{
			name: "with float weighted capacities and a launch template",
			config: &ec2types.SpotFleetRequestConfigData{
				LaunchTemplateConfigs: []ec2types.LaunchTemplateConfig{
					{
						Overrides: []ec2types.LaunchTemplateOverrides{
							{
								WeightedCapacity: aws.Float64(1.5),
							},
						},
					},
				},
				TargetCapacity: aws.Int32(1),
			},
		},
		{
			name: "with float weighted capacities and without a launch template",
			config: &ec2types.SpotFleetRequestConfigData{
				LaunchSpecifications: []ec2types.SpotFleetLaunchSpecification{
					{
						WeightedCapacity: aws.Float64(1.5),
					},
				},
				TargetCapacity: aws.Int32(1),
			},
		},
	}

	for _, tt := range exceptionTests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			ctx := context.Background()

			spotFleetRequestID := "sfr-39d27795-73f7-4c2d-976f-3262e0c988af"
			ec2Mock := capacitymock.NewMockEC2API(ctrl)
			drainerMock := capacitymock.NewMockDrainer(ctrl)
			pollerMock := capacitymock.NewMockPoller(ctrl)

			ec2Mock.EXPECT().DescribeSpotFleetRequests(ctx, gomock.Any()).Return(&ec2.DescribeSpotFleetRequestsOutput{
				SpotFleetRequestConfigs: []ec2types.SpotFleetRequestConfig{
					{
						SpotFleetRequestId:     aws.String(spotFleetRequestID),
						SpotFleetRequestConfig: tt.config,
					},
				},
			}, nil)

			sfr, err := capacity.NewSpotFleetRequest(spotFleetRequestID, ec2Mock)
			if err != nil {
				t.Fatal(err)
			}

			if err := sfr.ReduceCapacity(ctx, 4, drainerMock, pollerMock); err == nil {
				t.Errorf("err = nil; want non-nil")
			}
		})
	}
}
