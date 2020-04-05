package capacity_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/sqs"
	"github.com/golang/mock/gomock"

	"github.com/abicky/ecsmec/internal/capacity"
	"github.com/abicky/ecsmec/internal/testing/mocks"
)

func TestSpotFleetRequest_TerminateAllInstances(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	spotFleetRequestID := "sfr-39d27795-73f7-4c2d-976f-3262e0c988af"

	instances := append(
		createInstances("ap-northeast-1a", 1),
		createInstances("ap-northeast-1c", 1)...,
	)

	activeInstances := make([]*ec2.ActiveInstance, len(instances))
	for i, instance := range instances {
		activeInstances[i] = &ec2.ActiveInstance{
			InstanceId:            instance.InstanceId,
			SpotInstanceRequestId: aws.String(spotFleetRequestID),
		}
	}

	t.Run("with the state cancelled_running", func(t *testing.T) {
		ec2Mock := mocks.NewMockEC2API(ctrl)
		drainerMock := mocks.NewMockDrainer(ctrl)

		gomock.InOrder(
			ec2Mock.EXPECT().DescribeSpotFleetRequests(gomock.Any()).Return(&ec2.DescribeSpotFleetRequestsOutput{
				SpotFleetRequestConfigs: []*ec2.SpotFleetRequestConfig{
					{
						SpotFleetRequestConfig: &ec2.SpotFleetRequestConfigData{
							Type: aws.String("maintain"),
						},
						SpotFleetRequestId:    aws.String(spotFleetRequestID),
						SpotFleetRequestState: aws.String("cancelled_running"),
					},
				},
			}, nil),

			ec2Mock.EXPECT().DescribeSpotFleetInstances(gomock.Any()).Return(&ec2.DescribeSpotFleetInstancesOutput{
				ActiveInstances:    activeInstances,
				SpotFleetRequestId: aws.String(spotFleetRequestID),
			}, nil),

			drainerMock.EXPECT().Drain(gomock.Len(len(instances))),

			ec2Mock.EXPECT().TerminateInstances(gomock.Any()).Do(func(input *ec2.TerminateInstancesInput) {
				want := make([]string, len(instances))
				for i, instance := range instances {
					want[i] = *instance.InstanceId
				}
				got := aws.StringValueSlice(input.InstanceIds)
				if !reflect.DeepEqual(got, want) {
					t.Errorf("aws.StringValueSlice(input.InstanceIds) = %#v; want %#v", got, want)
				}
			}),

			ec2Mock.EXPECT().WaitUntilInstanceTerminated(gomock.Any()),
		)

		sfr, err := capacity.NewSpotFleetRequest(spotFleetRequestID, ec2Mock)
		if err != nil {
			t.Fatal(err)
		}

		err = sfr.TerminateAllInstances(drainerMock)
		if err != nil {
			t.Errorf("err = %#v; want nil", err)
		}
	})

	t.Run("with the state active", func(t *testing.T) {
		ec2Mock := mocks.NewMockEC2API(ctrl)
		drainerMock := mocks.NewMockDrainer(ctrl)

		gomock.InOrder(
			ec2Mock.EXPECT().DescribeSpotFleetRequests(gomock.Any()).Return(&ec2.DescribeSpotFleetRequestsOutput{
				SpotFleetRequestConfigs: []*ec2.SpotFleetRequestConfig{
					{
						SpotFleetRequestConfig: &ec2.SpotFleetRequestConfigData{
							Type: aws.String("maintain"),
						},
						SpotFleetRequestId:    aws.String(spotFleetRequestID),
						SpotFleetRequestState: aws.String("active"),
					},
				},
			}, nil),

			ec2Mock.EXPECT().DescribeSpotFleetInstances(gomock.Any()).Return(&ec2.DescribeSpotFleetInstancesOutput{
				ActiveInstances:    activeInstances,
				SpotFleetRequestId: aws.String(spotFleetRequestID),
			}, nil),
		)

		sfr, err := capacity.NewSpotFleetRequest(spotFleetRequestID, ec2Mock)
		if err != nil {
			t.Fatal(err)
		}

		err = sfr.TerminateAllInstances(drainerMock)
		if err == nil {
			t.Errorf("err = nil; want non-nil")
		}
	})
}

func TestSpotFleetRequest_ReduceCapacity(t *testing.T) {
	tests := []struct {
		name                 string
		drainedInstanceCount int
		finalTargetCapacity  int64
		amount               int64
		config               *ec2.SpotFleetRequestConfigData
	}{
		{
			name:                 "without weighted capacity in the launch template configs",
			drainedInstanceCount: 3,
			finalTargetCapacity:  7,
			amount:               3,
			config: &ec2.SpotFleetRequestConfigData{
				LaunchTemplateConfigs: []*ec2.LaunchTemplateConfig{
					{},
				},
				TargetCapacity: aws.Int64(10),
			},
		},
		{
			name:                 "with weighted capacity in the launch template configs",
			drainedInstanceCount: 1,
			finalTargetCapacity:  7,
			amount:               3,
			config: &ec2.SpotFleetRequestConfigData{
				LaunchTemplateConfigs: []*ec2.LaunchTemplateConfig{
					{
						Overrides: []*ec2.LaunchTemplateOverrides{
							{
								WeightedCapacity: aws.Float64(2),
							},
						},
					},
				},
				TargetCapacity: aws.Int64(10),
			},
		},
		{
			name:                 "without weighed capacity in the launch specifications",
			drainedInstanceCount: 3,
			finalTargetCapacity:  7,
			amount:               3,
			config: &ec2.SpotFleetRequestConfigData{
				LaunchSpecifications: []*ec2.SpotFleetLaunchSpecification{
					{},
				},
				TargetCapacity: aws.Int64(10),
			},
		},
		{
			name:                 "with weighed capacity in the launch specifications",
			drainedInstanceCount: 1,
			finalTargetCapacity:  7,
			amount:               3,
			config: &ec2.SpotFleetRequestConfigData{
				LaunchSpecifications: []*ec2.SpotFleetLaunchSpecification{
					{
						WeightedCapacity: aws.Float64(2),
					},
				},
				TargetCapacity: aws.Int64(10),
			},
		},
		{
			name:                 "when the amount is greater than the current target capacity",
			drainedInstanceCount: 1,
			finalTargetCapacity:  0,
			amount:               2,
			config: &ec2.SpotFleetRequestConfigData{
				LaunchTemplateConfigs: []*ec2.LaunchTemplateConfig{
					{},
				},
				TargetCapacity: aws.Int64(1),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			spotFleetRequestID := "sfr-39d27795-73f7-4c2d-976f-3262e0c988af"
			ec2Mock := mocks.NewMockEC2API(ctrl)
			drainerMock := mocks.NewMockDrainer(ctrl)
			pollerMock := mocks.NewMockPoller(ctrl)

			messages := make([]*sqs.Message, tt.drainedInstanceCount)
			entries := make([]*sqs.DeleteMessageBatchRequestEntry, tt.drainedInstanceCount)

			pollerMock.EXPECT().Poll(gomock.Any(), gomock.Any()).Do(func(ctx context.Context, fn func([]*sqs.Message) ([]*sqs.DeleteMessageBatchRequestEntry, error)) {
				fn(messages)
			})

			gomock.InOrder(
				ec2Mock.EXPECT().DescribeSpotFleetRequests(gomock.Any()).Return(&ec2.DescribeSpotFleetRequestsOutput{
					SpotFleetRequestConfigs: []*ec2.SpotFleetRequestConfig{
						{
							SpotFleetRequestId:     aws.String(spotFleetRequestID),
							SpotFleetRequestConfig: tt.config,
						},
					},
				}, nil),

				ec2Mock.EXPECT().ModifySpotFleetRequest(gomock.Any()).Do(func(input *ec2.ModifySpotFleetRequestInput) {
					if *input.TargetCapacity != tt.finalTargetCapacity {
						t.Errorf("*input.TargetCapacity = %d; want %d", *input.TargetCapacity, tt.finalTargetCapacity)
					}
				}),

				drainerMock.EXPECT().ProcessInterruptions(messages).Return(entries, nil),
			)

			sfr, err := capacity.NewSpotFleetRequest(spotFleetRequestID, ec2Mock)
			if err != nil {
				t.Fatal(err)
			}

			err = sfr.ReduceCapacity(tt.amount, drainerMock, pollerMock)
			if err != nil {
				t.Errorf("err = %#v; want nil", err)
			}
		})
	}

	t.Run("when the target capacity is already 0", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		spotFleetRequestID := "sfr-39d27795-73f7-4c2d-976f-3262e0c988af"
		ec2Mock := mocks.NewMockEC2API(ctrl)
		drainerMock := mocks.NewMockDrainer(ctrl)
		pollerMock := mocks.NewMockPoller(ctrl)

		ec2Mock.EXPECT().DescribeSpotFleetRequests(gomock.Any()).Return(&ec2.DescribeSpotFleetRequestsOutput{
			SpotFleetRequestConfigs: []*ec2.SpotFleetRequestConfig{
				{
					SpotFleetRequestId: aws.String(spotFleetRequestID),
					SpotFleetRequestConfig: &ec2.SpotFleetRequestConfigData{
						TargetCapacity: aws.Int64(0),
					},
				},
			},
		}, nil)

		sfr, err := capacity.NewSpotFleetRequest(spotFleetRequestID, ec2Mock)
		if err != nil {
			t.Fatal(err)
		}

		err = sfr.ReduceCapacity(1, drainerMock, pollerMock)
		if err != nil {
			t.Errorf("err = %#v; want nil", err)
		}
	})

	exceptionTests := []struct {
		name   string
		config *ec2.SpotFleetRequestConfigData
	}{
		{
			name: "with mixed weighted capacities and a launch template",
			config: &ec2.SpotFleetRequestConfigData{
				LaunchTemplateConfigs: []*ec2.LaunchTemplateConfig{
					{
						Overrides: []*ec2.LaunchTemplateOverrides{
							{
								WeightedCapacity: aws.Float64(1),
							},
							{
								WeightedCapacity: aws.Float64(2),
							},
						},
					},
				},
				TargetCapacity: aws.Int64(1),
			},
		},
		{
			name: "with mixed weighted capacities and without a launch template",
			config: &ec2.SpotFleetRequestConfigData{
				LaunchSpecifications: []*ec2.SpotFleetLaunchSpecification{
					{
						WeightedCapacity: aws.Float64(1),
					},
					{
						WeightedCapacity: aws.Float64(2),
					},
				},
				TargetCapacity: aws.Int64(1),
			},
		},
		{
			name: "with float weighted capacities and a launch template",
			config: &ec2.SpotFleetRequestConfigData{
				LaunchTemplateConfigs: []*ec2.LaunchTemplateConfig{
					{
						Overrides: []*ec2.LaunchTemplateOverrides{
							{
								WeightedCapacity: aws.Float64(1.5),
							},
						},
					},
				},
				TargetCapacity: aws.Int64(1),
			},
		},
		{
			name: "with float weighted capacities and without a launch template",
			config: &ec2.SpotFleetRequestConfigData{
				LaunchSpecifications: []*ec2.SpotFleetLaunchSpecification{
					{
						WeightedCapacity: aws.Float64(1.5),
					},
				},
				TargetCapacity: aws.Int64(1),
			},
		},
	}

	for _, tt := range exceptionTests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			spotFleetRequestID := "sfr-39d27795-73f7-4c2d-976f-3262e0c988af"
			ec2Mock := mocks.NewMockEC2API(ctrl)
			drainerMock := mocks.NewMockDrainer(ctrl)
			pollerMock := mocks.NewMockPoller(ctrl)

			ec2Mock.EXPECT().DescribeSpotFleetRequests(gomock.Any()).Return(&ec2.DescribeSpotFleetRequestsOutput{
				SpotFleetRequestConfigs: []*ec2.SpotFleetRequestConfig{
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

			err = sfr.ReduceCapacity(4, drainerMock, pollerMock)
			if err == nil {
				t.Errorf("err = nil; want non-nil")
			}
		})
	}
}
