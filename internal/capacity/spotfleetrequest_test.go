package capacity_test

import (
	"reflect"
	"testing"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/ec2"
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
