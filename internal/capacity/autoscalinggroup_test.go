package capacity_test

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	autoscalingtypes "github.com/aws/aws-sdk-go-v2/service/autoscaling/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"go.uber.org/mock/gomock"

	"github.com/abicky/ecsmec/internal/capacity"
	"github.com/abicky/ecsmec/internal/const/autoscalingconst"
	"github.com/abicky/ecsmec/internal/testing/capacitymock"
	"github.com/abicky/ecsmec/internal/testing/testutil"
)

func createReservation(instance autoscalingtypes.Instance, launchTime time.Time) ec2types.Reservation {
	return createReservations([]autoscalingtypes.Instance{instance}, launchTime)[0]
}

func createReservations(instances []autoscalingtypes.Instance, launchTime time.Time) []ec2types.Reservation {
	reservations := make([]ec2types.Reservation, len(instances))
	for i, instance := range instances {
		reservations[i] = ec2types.Reservation{
			Instances: []ec2types.Instance{
				{
					InstanceId: instance.InstanceId,
					LaunchTime: aws.Time(launchTime),
					Placement: &ec2types.Placement{
						AvailabilityZone: instance.AvailabilityZone,
					},
				},
			},
		}
	}

	return reservations
}

func expectLaunchNewInstances(
	t *testing.T,
	ctx context.Context,
	asMock *capacitymock.MockAutoScalingAPI,
	existingInstances, newInstances []autoscalingtypes.Instance,
	desiredCapacity, maxSize int32,
	stateSavedAt string,
) *gomock.Call {
	t.Helper()

	newDesiredCapacity := desiredCapacity + int32(len(newInstances))
	expectedStateSavedAt, err := time.Parse(time.RFC3339, stateSavedAt)
	if err != nil {
		t.Fatalf("stateSavedAt is invalid format: %s", stateSavedAt)
	}

	azs := make([]string, 0)
	for _, i := range existingInstances {
		if !slices.Contains(azs, *i.AvailabilityZone) {
			azs = append(azs, *i.AvailabilityZone)
		}
	}

	return testutil.InOrder(
		asMock.EXPECT().DescribeAutoScalingGroups(ctx, gomock.Any()).Return(&autoscaling.DescribeAutoScalingGroupsOutput{
			AutoScalingGroups: []autoscalingtypes.AutoScalingGroup{
				{
					AutoScalingGroupName: aws.String("autoscaling-group-name"),
					AvailabilityZones:    azs,
					DesiredCapacity:      aws.Int32(desiredCapacity),
					Instances:            existingInstances,
					MaxSize:              aws.Int32(maxSize),
				},
			},
		}, nil),

		asMock.EXPECT().CreateOrUpdateTags(ctx, gomock.Any()).Do(func(_ context.Context, input *autoscaling.CreateOrUpdateTagsInput, _ ...func(*ecs.Options)) {
			if len(input.Tags) != 3 {
				t.Errorf("len(input.Tags) = %d; want %d", len(input.Tags), 3)
			}
			for _, tag := range input.Tags {
				switch *tag.Key {
				case "ecsmec:OriginalDesiredCapacity":
					if *tag.Value != fmt.Sprint(desiredCapacity) {
						t.Errorf("ecsmec:OriginalDesiredCapacity = %s; want %d", *tag.Value, desiredCapacity)
					}
				case "ecsmec:OriginalMaxSize":
					if *tag.Value != fmt.Sprint(maxSize) {
						t.Errorf("ecsmec:OriginalMaxSize = %s; want %d", *tag.Value, maxSize)
					}
				case "ecsmec:StateSavedAt":
					savedAt, err := time.Parse(time.RFC3339, *tag.Value)
					if err != nil {
						t.Errorf("ecsmec:StateSavedAt is invalid format: %s: %s", *tag.Value, err)
						continue
					}
					if savedAt.After(expectedStateSavedAt) || savedAt.Before(expectedStateSavedAt.Add(-time.Minute)) {
						t.Errorf("ecsmec:StateSavedAt = %v; want around %v", savedAt, time.Now())
					}
				default:
					t.Errorf("unknown tag %s", *tag.Key)
				}
			}
		}),

		asMock.EXPECT().UpdateAutoScalingGroup(ctx, gomock.Any()).Do(func(_ context.Context, input *autoscaling.UpdateAutoScalingGroupInput, _ ...func(*autoscaling.Options)) {
			if *input.DesiredCapacity != newDesiredCapacity {
				t.Errorf("DesiredCapacity = %d; want %d", *input.DesiredCapacity, newDesiredCapacity)
			}
			if *input.MaxSize != newDesiredCapacity {
				t.Errorf("MaxSize = %d; want %d", *input.MaxSize, newDesiredCapacity)
			}
		}),

		// For `waitUntilInstancesInService` and `reload` at the end of the method
		asMock.EXPECT().DescribeAutoScalingGroups(ctx, gomock.Any()).Times(2).Return(&autoscaling.DescribeAutoScalingGroupsOutput{
			AutoScalingGroups: []autoscalingtypes.AutoScalingGroup{
				{
					AutoScalingGroupName: aws.String("autoscaling-group-name"),
					DesiredCapacity:      aws.Int32(newDesiredCapacity),
					Instances:            append(existingInstances, newInstances...),
					MaxSize:              aws.Int32(newDesiredCapacity),
					Tags: []autoscalingtypes.TagDescription{
						{
							Key:   aws.String("ecsmec:OriginalDesiredCapacity"),
							Value: aws.String(fmt.Sprint(desiredCapacity)),
						},
						{
							Key:   aws.String("ecsmec:OriginalMaxSize"),
							Value: aws.String(fmt.Sprint(maxSize)),
						},
						{
							Key:   aws.String("ecsmec:StateSavedAt"),
							Value: aws.String(stateSavedAt),
						},
					},
				},
			},
		}, nil),
	)
}

func expectTerminateInstances(
	t *testing.T,
	ctx context.Context,
	asMock *capacitymock.MockAutoScalingAPI,
	ec2Mock *capacitymock.MockEC2API,
	drainerMock *capacitymock.MockDrainer,
	instancesToTerminate, instancesToKeep []autoscalingtypes.Instance,
	reservationsToTerminate, reservationsToKeep []ec2types.Reservation,
	desiredCapacity, maxSize int32,
) *gomock.Call {
	t.Helper()

	return testutil.InOrder(
		ec2Mock.EXPECT().DescribeInstances(ctx, gomock.Any()).Return(&ec2.DescribeInstancesOutput{
			Reservations: append(reservationsToTerminate, reservationsToKeep...),
		}, nil),

		drainerMock.EXPECT().Drain(ctx, gomock.Len(len(instancesToTerminate))),

		asMock.EXPECT().DetachInstances(ctx, gomock.Any()).Do(func(_ context.Context, input *autoscaling.DetachInstancesInput, _ ...func(*autoscaling.Options)) {
			want := make([]string, len(instancesToTerminate))
			for i, instance := range instancesToTerminate {
				want[i] = *instance.InstanceId
			}
			if !testutil.MatchSlice(want, input.InstanceIds) {
				t.Errorf("input.InstanceIds = %v; want %v", input.InstanceIds, want)
			}
		}),

		ec2Mock.EXPECT().TerminateInstances(ctx, gomock.Any()).Do(func(_ context.Context, input *ec2.TerminateInstancesInput, _ ...func(options *ec2.Options)) {
			want := make([]string, len(instancesToTerminate))
			for i, instance := range instancesToTerminate {
				want[i] = *instance.InstanceId
			}
			if !testutil.MatchSlice(want, input.InstanceIds) {
				t.Errorf("input.InstanceIds = %v; want %v", input.InstanceIds, want)
			}
		}),

		// For InstanceTerminatedWaiter
		ec2Mock.EXPECT().DescribeInstances(testutil.AnyContext(), gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, input *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			instanceIds := make([]string, len(instancesToTerminate))
			instances := make([]ec2types.Instance, len(instancesToTerminate))
			for i, instance := range instancesToTerminate {
				instanceIds[i] = *instance.InstanceId
				instances[i] = ec2types.Instance{
					InstanceId: instance.InstanceId,
					State: &ec2types.InstanceState{
						Name: "terminated",
					},
				}
			}

			if !testutil.MatchSlice(input.InstanceIds, instanceIds) {
				t.Errorf("input.InstanceIds = %v; want %v", input.InstanceIds, instanceIds)
			}

			return &ec2.DescribeInstancesOutput{
				Reservations: []ec2types.Reservation{
					{
						Instances: instances,
					},
				},
			}, nil
		}),

		// Call `reload` at the end of the method
		asMock.EXPECT().DescribeAutoScalingGroups(ctx, gomock.Any()).Return(&autoscaling.DescribeAutoScalingGroupsOutput{
			AutoScalingGroups: []autoscalingtypes.AutoScalingGroup{
				{
					AutoScalingGroupName: aws.String("autoscaling-group-name"),
					DesiredCapacity:      aws.Int32(desiredCapacity),
					Instances:            instancesToKeep,
					MaxSize:              aws.Int32(maxSize),
				},
			},
		}, nil),
	)
}

func expectRestoreState(
	t *testing.T,
	ctx context.Context,
	asMock *capacitymock.MockAutoScalingAPI,
	desiredCapacity, maxSize int32,
	stateSavedAt string,
) *gomock.Call {
	t.Helper()

	return testutil.InOrder(
		asMock.EXPECT().UpdateAutoScalingGroup(ctx, gomock.Any()).Do(func(_ context.Context, input *autoscaling.UpdateAutoScalingGroupInput, _ ...func(*autoscaling.Options)) {
			if input.DesiredCapacity != nil {
				t.Errorf("DesiredCapacity = %d; want nil", *input.DesiredCapacity)
			}
			if *input.MaxSize != maxSize {
				t.Errorf("MaxSize = %d; want %d", *input.MaxSize, maxSize)
			}
		}),

		asMock.EXPECT().DeleteTags(ctx, gomock.Any()).Do(func(_ context.Context, input *autoscaling.DeleteTagsInput, _ ...func(*autoscaling.Options)) {
			if len(input.Tags) != 3 {
				t.Errorf("len(input.Tags) = %d; want %d", len(input.Tags), 3)
			}
			for _, tag := range input.Tags {
				switch *tag.Key {
				case "ecsmec:OriginalDesiredCapacity":
					if *tag.Value != fmt.Sprint(desiredCapacity) {
						t.Errorf("ecsmec:OriginalDesiredCapacity = %s; want %d", *tag.Value, desiredCapacity)
					}
				case "ecsmec:OriginalMaxSize":
					if *tag.Value != fmt.Sprint(maxSize) {
						t.Errorf("ecsmec:OriginalMaxSize = %s; want %d", *tag.Value, maxSize)
					}
				case "ecsmec:StateSavedAt":
					if *tag.Value != stateSavedAt {
						t.Errorf("ecsmec:StateSavedAt = %s; want %s", *tag.Value, stateSavedAt)
					}
				default:
					t.Errorf("unknown tag %s", *tag.Key)
				}
			}
		}),

		// Call `reload` at the end of the method
		asMock.EXPECT().DescribeAutoScalingGroups(ctx, gomock.Any()).Return(&autoscaling.DescribeAutoScalingGroupsOutput{
			AutoScalingGroups: []autoscalingtypes.AutoScalingGroup{
				{
					AutoScalingGroupName: aws.String("autoscaling-group-name"),
					DesiredCapacity:      aws.Int32(desiredCapacity),
					MaxSize:              aws.Int32(maxSize),
				},
			},
		}, nil),
	)
}

func TestNewAutoScalingGroup(t *testing.T) {
	t.Run("without tags", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		desiredCapacity := int32(5)
		maxSize := int32(10)

		asMock := capacitymock.NewMockAutoScalingAPI(ctrl)
		ec2Mock := capacitymock.NewMockEC2API(ctrl)

		asMock.EXPECT().DescribeAutoScalingGroups(gomock.Any(), gomock.Any()).Return(&autoscaling.DescribeAutoScalingGroupsOutput{
			AutoScalingGroups: []autoscalingtypes.AutoScalingGroup{
				{
					DesiredCapacity: aws.Int32(desiredCapacity),
					MaxSize:         aws.Int32(maxSize),
				},
			},
		}, nil)

		group, err := capacity.NewAutoScalingGroup("autoscaling-asg-name", asMock, ec2Mock)
		if err != nil {
			t.Fatal(err)
		}

		if *group.OriginalDesiredCapacity != desiredCapacity {
			t.Errorf("OriginalDesiredCapacity = %d; want %d", *group.OriginalDesiredCapacity, desiredCapacity)
		}
		if *group.OriginalMaxSize != maxSize {
			t.Errorf("OriginalMaxSize = %d; want %d", *group.OriginalMaxSize, maxSize)
		}
		if group.StateSavedAt != nil {
			t.Errorf("StateSavedAt = %v; want nil", group.StateSavedAt)
		}
	})

	t.Run("with tags", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		desiredCapacity := int32(5)
		maxSize := int32(8)
		increasedDesiredCapacity := desiredCapacity * 2
		stateSavedAt := time.Now().UTC().Truncate(time.Second)

		asMock := capacitymock.NewMockAutoScalingAPI(ctrl)
		ec2Mock := capacitymock.NewMockEC2API(ctrl)

		asMock.EXPECT().DescribeAutoScalingGroups(gomock.Any(), gomock.Any()).Return(&autoscaling.DescribeAutoScalingGroupsOutput{
			AutoScalingGroups: []autoscalingtypes.AutoScalingGroup{
				{
					DesiredCapacity: aws.Int32(increasedDesiredCapacity),
					MaxSize:         aws.Int32(increasedDesiredCapacity),
					Tags: []autoscalingtypes.TagDescription{
						{
							Key:   aws.String("ecsmec:OriginalDesiredCapacity"),
							Value: aws.String(fmt.Sprint(desiredCapacity)),
						},
						{
							Key:   aws.String("ecsmec:OriginalMaxSize"),
							Value: aws.String(fmt.Sprint(maxSize)),
						},
						{
							Key:   aws.String("ecsmec:StateSavedAt"),
							Value: aws.String(stateSavedAt.Format(time.RFC3339)),
						},
					},
				},
			},
		}, nil)

		group, err := capacity.NewAutoScalingGroup("autoscaling-asg-name", asMock, ec2Mock)
		if err != nil {
			t.Fatal(err)
		}

		if *group.OriginalDesiredCapacity != desiredCapacity {
			t.Errorf("OriginalDesiredCapacity = %d; want %d", *group.OriginalDesiredCapacity, desiredCapacity)
		}
		if *group.OriginalMaxSize != maxSize {
			t.Errorf("OriginalMaxSize = %d; want %d", *group.OriginalMaxSize, maxSize)
		}
		if *group.StateSavedAt != stateSavedAt {
			t.Errorf("StateSavedAt = %v; want %v", *group.StateSavedAt, stateSavedAt)
		}
	})

	t.Run("the auto scaling group doesn't exist", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		asMock := capacitymock.NewMockAutoScalingAPI(ctrl)
		ec2Mock := capacitymock.NewMockEC2API(ctrl)

		asMock.EXPECT().DescribeAutoScalingGroups(gomock.Any(), gomock.Any()).Return(&autoscaling.DescribeAutoScalingGroupsOutput{
			AutoScalingGroups: nil,
		}, nil)

		_, err := capacity.NewAutoScalingGroup("autoscaling-group-name", asMock, ec2Mock)
		if err == nil {
			t.Errorf("err = nil; want non-nil")
		}
	})

}

func TestAutoScalingGroup_ReplaceInstances(t *testing.T) {
	tests := []struct {
		name            string
		desiredCapacity int32
		maxSize         int32
		oldInstances    []autoscalingtypes.Instance
		newInstances    []autoscalingtypes.Instance
	}{
		{
			name:            "the desired capacity is a multiple of the number of availability zones",
			desiredCapacity: 6,
			maxSize:         8,
			oldInstances: append(
				createInstances("ap-northeast-1a", 3),
				createInstances("ap-northeast-1c", 3)...,
			),
			newInstances: append(
				createInstances("ap-northeast-1a", 3),
				createInstances("ap-northeast-1c", 3)...,
			),
		},
		{
			name:            "the desired capacity is not a multiple of the number of availability zones but the number of availability zone is only two",
			desiredCapacity: 7,
			maxSize:         8,
			oldInstances: append(
				createInstances("ap-northeast-1a", 3),
				createInstances("ap-northeast-1c", 4)...,
			),
			newInstances: append(
				createInstances("ap-northeast-1a", 4),
				createInstances("ap-northeast-1c", 3)...,
			),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			ctx := context.Background()

			asMock := capacitymock.NewMockAutoScalingAPI(ctrl)
			ec2Mock := capacitymock.NewMockEC2API(ctrl)
			drainerMock := capacitymock.NewMockDrainer(ctrl)

			now := time.Now().UTC()
			stateSavedAt := now.Format(time.RFC3339)

			oldReservations := createReservations(tt.oldInstances, now.Add(-24*time.Hour))
			newReservations := createReservations(tt.newInstances, now)

			gomock.InOrder(
				asMock.EXPECT().DescribeAutoScalingGroups(ctx, gomock.Any()).Return(&autoscaling.DescribeAutoScalingGroupsOutput{
					AutoScalingGroups: []autoscalingtypes.AutoScalingGroup{
						{
							AutoScalingGroupName: aws.String("autoscaling-group-name"),
							AvailabilityZones: []string{
								"ap-northeast-1a",
								"ap-northeast-1c",
							},
							DesiredCapacity: aws.Int32(tt.desiredCapacity),
							Instances:       tt.oldInstances,
							MaxSize:         aws.Int32(tt.maxSize),
						},
					},
				}, nil),

				ec2Mock.EXPECT().DescribeInstances(ctx, gomock.Any()).Return(&ec2.DescribeInstancesOutput{
					Reservations: oldReservations,
				}, nil),

				expectLaunchNewInstances(t, ctx, asMock, tt.oldInstances, tt.newInstances, tt.desiredCapacity, tt.maxSize, stateSavedAt),
				expectTerminateInstances(t, ctx, asMock, ec2Mock, drainerMock, tt.oldInstances, tt.newInstances, oldReservations, newReservations, tt.desiredCapacity, tt.maxSize),
				expectRestoreState(t, ctx, asMock, tt.desiredCapacity, tt.maxSize, stateSavedAt),
			)

			group, err := capacity.NewAutoScalingGroup("autoscaling-group-name", asMock, ec2Mock)
			if err != nil {
				t.Fatal(err)
			}

			if err := group.ReplaceInstances(ctx, drainerMock); err != nil {
				t.Errorf("err = %#v; want nil", err)
			}
		})
	}

	t.Run("the desired capacity is not a multiple of the number of availability zones", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		ctx := context.Background()

		desiredCapacity := int32(5)
		maxSize := int32(8)

		asMock := capacitymock.NewMockAutoScalingAPI(ctrl)
		ec2Mock := capacitymock.NewMockEC2API(ctrl)
		drainerMock := capacitymock.NewMockDrainer(ctrl)

		now := time.Now().UTC()
		stateSavedAt := now.Format(time.RFC3339)

		oldInstances := append(
			append(
				createInstances("ap-northeast-1a", 2),
				createInstances("ap-northeast-1c", 1)...,
			),
			createInstances("ap-northeast-1d", 2)...,
		)
		oldReservations := createReservations(oldInstances, now.Add(-24*time.Hour))

		instancesToKeep := append(
			append(
				createInstances("ap-northeast-1a", 2),
				createInstances("ap-northeast-1c", 2)...,
			),
			createInstances("ap-northeast-1d", 1)...,
		)
		reservationsToKeep := createReservations(instancesToKeep, now)

		oldestNewCInstance := createInstance("ap-northeast-1c")
		newInstances := append(instancesToKeep, oldestNewCInstance)
		instancesToTerminate := append(oldInstances, oldestNewCInstance)
		reservationsToTerminate := append(
			oldReservations,
			createReservation(oldestNewCInstance, now.Add(-time.Second)),
		)

		gomock.InOrder(
			asMock.EXPECT().DescribeAutoScalingGroups(ctx, gomock.Any()).Return(&autoscaling.DescribeAutoScalingGroupsOutput{
				AutoScalingGroups: []autoscalingtypes.AutoScalingGroup{
					{
						AutoScalingGroupName: aws.String("autoscaling-group-name"),
						AvailabilityZones: []string{
							"ap-northeast-1a",
							"ap-northeast-1c",
							"ap-northeast-1d",
						},
						DesiredCapacity: aws.Int32(desiredCapacity),
						Instances:       oldInstances,
						MaxSize:         aws.Int32(maxSize),
					},
				},
			}, nil),

			ec2Mock.EXPECT().DescribeInstances(ctx, gomock.Any()).Return(&ec2.DescribeInstancesOutput{
				Reservations: oldReservations,
			}, nil),

			expectLaunchNewInstances(t, ctx, asMock, oldInstances, newInstances, desiredCapacity, maxSize, stateSavedAt),
			expectTerminateInstances(t, ctx, asMock, ec2Mock, drainerMock, instancesToTerminate, instancesToKeep, reservationsToTerminate, reservationsToKeep, desiredCapacity, maxSize),
			expectRestoreState(t, ctx, asMock, desiredCapacity, maxSize, stateSavedAt),
		)

		group, err := capacity.NewAutoScalingGroup("autoscaling-group-name", asMock, ec2Mock)
		if err != nil {
			t.Fatal(err)
		}

		if err := group.ReplaceInstances(ctx, drainerMock); err != nil {
			t.Errorf("err = %#v; want nil", err)
		}
	})

	t.Run("replacement is already finished", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		ctx := context.Background()

		desiredCapacity := int32(6)
		maxSize := int32(8)

		asMock := capacitymock.NewMockAutoScalingAPI(ctrl)
		ec2Mock := capacitymock.NewMockEC2API(ctrl)
		drainerMock := capacitymock.NewMockDrainer(ctrl)

		now := time.Now().UTC()
		stateSavedAt := now.Format(time.RFC3339)

		instances := append(
			createInstances("ap-northeast-1a", int(desiredCapacity/2)),
			createInstances("ap-northeast-1c", int(desiredCapacity/2))...,
		)

		gomock.InOrder(
			asMock.EXPECT().DescribeAutoScalingGroups(ctx, gomock.Any()).Return(&autoscaling.DescribeAutoScalingGroupsOutput{
				AutoScalingGroups: []autoscalingtypes.AutoScalingGroup{
					{
						AutoScalingGroupName: aws.String("autoscaling-group-name"),
						AvailabilityZones: []string{
							"ap-northeast-1a",
							"ap-northeast-1c",
						},
						DesiredCapacity: aws.Int32(desiredCapacity),
						Instances:       instances,
						MaxSize:         aws.Int32(maxSize),
						Tags: []autoscalingtypes.TagDescription{
							{
								Key:   aws.String("ecsmec:OriginalDesiredCapacity"),
								Value: aws.String(fmt.Sprint(desiredCapacity)),
							},
							{
								Key:   aws.String("ecsmec:OriginalMaxSize"),
								Value: aws.String(fmt.Sprint(maxSize)),
							},
							{
								Key:   aws.String("ecsmec:StateSavedAt"),
								Value: aws.String(stateSavedAt),
							},
						},
					},
				},
			}, nil),

			ec2Mock.EXPECT().DescribeInstances(ctx, gomock.Any()).Return(&ec2.DescribeInstancesOutput{
				Reservations: createReservations(instances, now),
			}, nil),

			expectRestoreState(t, ctx, asMock, desiredCapacity, maxSize, stateSavedAt),
		)

		group, err := capacity.NewAutoScalingGroup("autoscaling-group-name", asMock, ec2Mock)
		if err != nil {
			t.Fatal(err)
		}

		if err := group.ReplaceInstances(ctx, drainerMock); err != nil {
			t.Errorf("err = %#v; want nil", err)
		}
	})

	t.Run("the process is resumed after new instances are launched", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		ctx := context.Background()

		desiredCapacity := int32(6)
		maxSize := int32(8)

		asMock := capacitymock.NewMockAutoScalingAPI(ctrl)
		ec2Mock := capacitymock.NewMockEC2API(ctrl)
		drainerMock := capacitymock.NewMockDrainer(ctrl)

		now := time.Now().UTC()
		stateSavedAt := now.Format(time.RFC3339)
		tags := []autoscalingtypes.TagDescription{
			{
				Key:   aws.String("ecsmec:OriginalDesiredCapacity"),
				Value: aws.String(fmt.Sprint(desiredCapacity)),
			},
			{
				Key:   aws.String("ecsmec:OriginalMaxSize"),
				Value: aws.String(fmt.Sprint(maxSize)),
			},
			{
				Key:   aws.String("ecsmec:StateSavedAt"),
				Value: aws.String(stateSavedAt),
			},
		}

		oldInstances := append(
			createInstances("ap-northeast-1a", int(desiredCapacity/2)),
			createInstances("ap-northeast-1c", int(desiredCapacity/2))...,
		)
		oldReservations := createReservations(oldInstances, now.Add(-24*time.Hour))

		newInstances := append(
			createInstances("ap-northeast-1a", int(desiredCapacity/2)),
			createInstances("ap-northeast-1c", int(desiredCapacity/2))...,
		)
		newReservations := createReservations(newInstances, now)

		gomock.InOrder(
			asMock.EXPECT().DescribeAutoScalingGroups(ctx, gomock.Any()).Times(2).Return(&autoscaling.DescribeAutoScalingGroupsOutput{
				AutoScalingGroups: []autoscalingtypes.AutoScalingGroup{
					{
						AutoScalingGroupName: aws.String("autoscaling-group-name"),
						AvailabilityZones: []string{
							"ap-northeast-1a",
							"ap-northeast-1c",
						},
						DesiredCapacity: aws.Int32(int32(len(oldInstances) + len(newInstances))),
						Instances:       append(oldInstances, newInstances...),
						MaxSize:         aws.Int32(int32(len(oldInstances) + len(newInstances))),
						Tags:            tags,
					},
				},
			}, nil),

			ec2Mock.EXPECT().DescribeInstances(ctx, gomock.Any()).Return(&ec2.DescribeInstancesOutput{
				Reservations: oldReservations,
			}, nil),

			expectTerminateInstances(t, ctx, asMock, ec2Mock, drainerMock, oldInstances, newInstances, oldReservations, newReservations, desiredCapacity, maxSize),
			expectRestoreState(t, ctx, asMock, desiredCapacity, maxSize, stateSavedAt),
		)

		group, err := capacity.NewAutoScalingGroup("autoscaling-group-name", asMock, ec2Mock)
		if err != nil {
			t.Fatal(err)
		}

		if err := group.ReplaceInstances(ctx, drainerMock); err != nil {
			t.Errorf("err = %#v; want nil", err)
		}
	})
}

func TestAutoScalingGroup_ReduceCapacity(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()

	asMock := capacitymock.NewMockAutoScalingAPI(ctrl)
	ec2Mock := capacitymock.NewMockEC2API(ctrl)
	drainerMock := capacitymock.NewMockDrainer(ctrl)

	now := time.Now().UTC()

	instancesToTerminate := append(
		append(
			createInstances("ap-northeast-1a", autoscalingconst.MaxDetachableInstances),
			createInstances("ap-northeast-1c", autoscalingconst.MaxDetachableInstances+1)...,
		),
		createInstances("ap-northeast-1d", autoscalingconst.MaxDetachableInstances)...,
	)
	reservationsToTerminate := createReservations(instancesToTerminate, now.Add(-24*time.Hour))

	instancesToKeep := append(
		append(
			createInstances("ap-northeast-1a", 2),
			createInstances("ap-northeast-1c", 2)...,
		),
		createInstances("ap-northeast-1d", 2)...,
	)
	reservationsToKeep := createReservations(instancesToKeep, now)

	allInstances := append(instancesToTerminate, instancesToKeep...)
	detachedInstanceIds := make([]string, 0, len(instancesToTerminate))
	terminatedInstanceIds := make([]string, 0, len(instancesToTerminate))

	gomock.InOrder(
		asMock.EXPECT().DescribeAutoScalingGroups(ctx, gomock.Any()).Return(&autoscaling.DescribeAutoScalingGroupsOutput{
			AutoScalingGroups: []autoscalingtypes.AutoScalingGroup{
				{
					AutoScalingGroupName: aws.String("autoscaling-group-name"),
					DesiredCapacity:      aws.Int32(int32(len(allInstances))),
					Instances:            allInstances,
					MaxSize:              aws.Int32(int32(len(allInstances))),
				},
			},
		}, nil),

		ec2Mock.EXPECT().DescribeInstances(ctx, gomock.Any()).Return(&ec2.DescribeInstancesOutput{
			Reservations: append(reservationsToTerminate, reservationsToKeep...),
		}, nil),

		drainerMock.EXPECT().Drain(ctx, gomock.Len(len(instancesToTerminate))),

		asMock.EXPECT().DetachInstances(ctx, gomock.Any()).Times(4).Do(func(_ context.Context, input *autoscaling.DetachInstancesInput, _ ...func(options *autoscaling.Options)) {
			detachedInstanceIds = append(detachedInstanceIds, input.InstanceIds...)
		}),

		ec2Mock.EXPECT().TerminateInstances(ctx, gomock.Any()).Do(func(_ context.Context, input *ec2.TerminateInstancesInput, _ ...func(*ec2.Options)) {
			terminatedInstanceIds = append(terminatedInstanceIds, input.InstanceIds...)
		}),

		// For InstanceTerminatedWaiter
		ec2Mock.EXPECT().DescribeInstances(testutil.AnyContext(), gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, input *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			if !testutil.MatchSlice(input.InstanceIds, terminatedInstanceIds) {
				t.Errorf("input.InstanceIds = %v; want %v", input.InstanceIds, terminatedInstanceIds)
			}

			instances := make([]ec2types.Instance, len(terminatedInstanceIds))
			for i, id := range terminatedInstanceIds {
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

		// Call `reload` at the end of the method
		asMock.EXPECT().DescribeAutoScalingGroups(ctx, gomock.Any()).Return(&autoscaling.DescribeAutoScalingGroupsOutput{
			AutoScalingGroups: []autoscalingtypes.AutoScalingGroup{
				{
					AutoScalingGroupName: aws.String("autoscaling-group-name"),
					DesiredCapacity:      aws.Int32(int32(len(instancesToKeep))),
					Instances:            instancesToKeep,
					MaxSize:              aws.Int32(int32(len(allInstances))),
				},
			},
		}, nil),
	)

	group, err := capacity.NewAutoScalingGroup("autoscaling-group-name", asMock, ec2Mock)
	if err != nil {
		t.Fatal(err)
	}

	if err := group.ReduceCapacity(ctx, int32(len(instancesToTerminate)), drainerMock); err != nil {
		t.Errorf("err = %#v; want nil", err)
	}

	instanceIdsToTerminate := make([]string, len(instancesToTerminate))
	for i, instance := range instancesToTerminate {
		instanceIdsToTerminate[i] = *instance.InstanceId
	}
	if !testutil.MatchSlice(detachedInstanceIds, instanceIdsToTerminate) {
		t.Errorf("detachedInstanceIds = %v; want %v", detachedInstanceIds, instanceIdsToTerminate)
	}
	if !testutil.MatchSlice(terminatedInstanceIds, instanceIdsToTerminate) {
		t.Errorf("terminatedInstanceIds = %v; want %v", terminatedInstanceIds, instanceIdsToTerminate)
	}

}
