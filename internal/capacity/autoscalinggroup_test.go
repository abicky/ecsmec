package capacity_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/autoscaling"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/golang/mock/gomock"

	"github.com/abicky/ecsmec/internal/capacity"
	"github.com/abicky/ecsmec/internal/const/autoscalingconst"
	"github.com/abicky/ecsmec/internal/sliceutil"
	"github.com/abicky/ecsmec/internal/testing/mocks"
)

func inOrder(calls ...*gomock.Call) *gomock.Call {
	gomock.InOrder(calls...)
	return calls[len(calls)-1]
}

func matchSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	aMap := make(map[string]int, len(a))
	for _, s := range a {
		aMap[s]++
	}
	for _, s := range b {
		if _, ok := aMap[s]; !ok {
			return false
		}
		aMap[s]--
		if aMap[s] == 0 {
			delete(aMap, s)
		}
	}

	if len(aMap) > 0 {
		return false
	}

	return true
}

func createReservation(instance *autoscaling.Instance, launchTime time.Time) *ec2.Reservation {
	return createReservations([]*autoscaling.Instance{instance}, launchTime)[0]
}

func createReservations(instances []*autoscaling.Instance, launchTime time.Time) []*ec2.Reservation {
	reservations := make([]*ec2.Reservation, len(instances))
	for i, instance := range instances {
		reservations[i] = &ec2.Reservation{
			Instances: []*ec2.Instance{
				{
					InstanceId: instance.InstanceId,
					LaunchTime: aws.Time(launchTime),
					Placement: &ec2.Placement{
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
	asMock *mocks.MockAutoScalingAPI,
	existingInstances, newInstances []*autoscaling.Instance,
	desiredCapacity, maxSize int64,
	stateSavedAt string,
) *gomock.Call {
	t.Helper()

	newDesiredCapacity := desiredCapacity + int64(len(newInstances))
	expectedStateSavedAt, err := time.Parse(time.RFC3339, stateSavedAt)
	if err != nil {
		t.Fatalf("stateSavedAt is invalid format: %s", stateSavedAt)
	}

	azs := make([]string, 0)
	for _, i := range existingInstances {
		if !sliceutil.Contains(azs, *i.AvailabilityZone) {
			azs = append(azs, *i.AvailabilityZone)
		}
	}

	return inOrder(
		asMock.EXPECT().DescribeAutoScalingGroups(gomock.Any()).Return(&autoscaling.DescribeAutoScalingGroupsOutput{
			AutoScalingGroups: []*autoscaling.Group{
				{
					AutoScalingGroupName: aws.String("autoscaling-group-name"),
					AvailabilityZones:    aws.StringSlice(azs),
					DesiredCapacity:      aws.Int64(desiredCapacity),
					Instances:            existingInstances,
					MaxSize:              aws.Int64(maxSize),
				},
			},
		}, nil),

		asMock.EXPECT().CreateOrUpdateTags(gomock.Any()).Do(func(input *autoscaling.CreateOrUpdateTagsInput) {
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

		asMock.EXPECT().UpdateAutoScalingGroup(gomock.Any()).Do(func(input *autoscaling.UpdateAutoScalingGroupInput) {
			if *input.DesiredCapacity != newDesiredCapacity {
				t.Errorf("DesiredCapacity = %d; want %d", *input.DesiredCapacity, newDesiredCapacity)
			}
			if *input.MaxSize != newDesiredCapacity {
				t.Errorf("MaxSize = %d; want %d", *input.MaxSize, newDesiredCapacity)
			}
		}),

		// For `waitUntilInstancesInService` and `reload` at the end of the method
		asMock.EXPECT().DescribeAutoScalingGroups(gomock.Any()).Times(2).Return(&autoscaling.DescribeAutoScalingGroupsOutput{
			AutoScalingGroups: []*autoscaling.Group{
				{
					AutoScalingGroupName: aws.String("autoscaling-group-name"),
					DesiredCapacity:      aws.Int64(newDesiredCapacity),
					Instances:            append(existingInstances, newInstances...),
					MaxSize:              aws.Int64(newDesiredCapacity),
					Tags: []*autoscaling.TagDescription{
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
	asMock *mocks.MockAutoScalingAPI,
	ec2Mock *mocks.MockEC2API,
	drainerMock *mocks.MockDrainer,
	instancesToTerminate, instancesToKeep []*autoscaling.Instance,
	reservationsToTerminate, reservationsToKeep []*ec2.Reservation,
	desiredCapacity, maxSize int64,
) *gomock.Call {
	t.Helper()

	return inOrder(
		ec2Mock.EXPECT().DescribeInstances(gomock.Any()).Return(&ec2.DescribeInstancesOutput{
			Reservations: append(reservationsToTerminate, reservationsToKeep...),
		}, nil),

		drainerMock.EXPECT().Drain(gomock.Len(len(instancesToTerminate))),

		asMock.EXPECT().DetachInstances(gomock.Any()).Do(func(input *autoscaling.DetachInstancesInput) {
			want := make([]string, len(instancesToTerminate))
			for i, instance := range instancesToTerminate {
				want[i] = *instance.InstanceId
			}
			got := aws.StringValueSlice(input.InstanceIds)
			if !matchSlice(want, got) {
				t.Errorf("input.InstanceIds = %v; want %v", got, want)
			}
		}),

		ec2Mock.EXPECT().TerminateInstances(gomock.Any()).Do(func(input *ec2.TerminateInstancesInput) {
			want := make([]string, len(instancesToTerminate))
			for i, instance := range instancesToTerminate {
				want[i] = *instance.InstanceId
			}
			got := aws.StringValueSlice(input.InstanceIds)
			if !matchSlice(want, got) {
				t.Errorf("input.InstanceIds = %v; want %v", got, want)
			}
		}),

		ec2Mock.EXPECT().WaitUntilInstanceTerminated(gomock.Any()),

		// Call `reload` at the end of the method
		asMock.EXPECT().DescribeAutoScalingGroups(gomock.Any()).Return(&autoscaling.DescribeAutoScalingGroupsOutput{
			AutoScalingGroups: []*autoscaling.Group{
				{
					AutoScalingGroupName: aws.String("autoscaling-group-name"),
					DesiredCapacity:      aws.Int64(desiredCapacity),
					Instances:            instancesToKeep,
					MaxSize:              aws.Int64(maxSize),
				},
			},
		}, nil),
	)
}

func expectRestoreState(
	t *testing.T,
	asMock *mocks.MockAutoScalingAPI,
	desiredCapacity, maxSize int64,
	stateSavedAt string,
) *gomock.Call {
	t.Helper()

	return inOrder(
		asMock.EXPECT().UpdateAutoScalingGroup(gomock.Any()).Do(func(input *autoscaling.UpdateAutoScalingGroupInput) {
			if input.DesiredCapacity != nil {
				t.Errorf("DesiredCapacity = %d; want nil", *input.DesiredCapacity)
			}
			if *input.MaxSize != maxSize {
				t.Errorf("MaxSize = %d; want %d", *input.MaxSize, maxSize)
			}
		}),

		asMock.EXPECT().DeleteTags(gomock.Any()).Do(func(input *autoscaling.DeleteTagsInput) {
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
		asMock.EXPECT().DescribeAutoScalingGroups(gomock.Any()).Return(&autoscaling.DescribeAutoScalingGroupsOutput{
			AutoScalingGroups: []*autoscaling.Group{
				{
					AutoScalingGroupName: aws.String("autoscaling-group-name"),
					DesiredCapacity:      aws.Int64(desiredCapacity),
					MaxSize:              aws.Int64(maxSize),
				},
			},
		}, nil),
	)
}

func TestNewAutoScalingGroup(t *testing.T) {
	t.Run("without tags", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		desiredCapacity := int64(5)
		maxSize := int64(10)

		asMock := mocks.NewMockAutoScalingAPI(ctrl)
		ec2Mock := mocks.NewMockEC2API(ctrl)

		asMock.EXPECT().DescribeAutoScalingGroups(gomock.Any()).Return(&autoscaling.DescribeAutoScalingGroupsOutput{
			AutoScalingGroups: []*autoscaling.Group{
				{
					DesiredCapacity: aws.Int64(desiredCapacity),
					MaxSize:         aws.Int64(maxSize),
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

		desiredCapacity := int64(5)
		maxSize := int64(8)
		increasedDesiredCapacity := desiredCapacity * 2
		stateSavedAt := time.Now().UTC().Truncate(time.Second)

		asMock := mocks.NewMockAutoScalingAPI(ctrl)
		ec2Mock := mocks.NewMockEC2API(ctrl)

		asMock.EXPECT().DescribeAutoScalingGroups(gomock.Any()).Return(&autoscaling.DescribeAutoScalingGroupsOutput{
			AutoScalingGroups: []*autoscaling.Group{
				{
					DesiredCapacity: aws.Int64(increasedDesiredCapacity),
					MaxSize:         aws.Int64(increasedDesiredCapacity),
					Tags: []*autoscaling.TagDescription{
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

		asMock := mocks.NewMockAutoScalingAPI(ctrl)
		ec2Mock := mocks.NewMockEC2API(ctrl)

		asMock.EXPECT().DescribeAutoScalingGroups(gomock.Any()).Return(&autoscaling.DescribeAutoScalingGroupsOutput{
			AutoScalingGroups: nil,
		}, nil)

		_, err := capacity.NewAutoScalingGroup("autoscaling-group-name", asMock, ec2Mock)
		if err == nil {
			t.Errorf("err = nil; want non-nil")
		}
	})

}

func TestAutoScalingGroup_ReplaceInstances(t *testing.T) {
	t.Run("the desired capacity is a multiple of the number of availability zones", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		desiredCapacity := int64(6)
		maxSize := int64(8)

		asMock := mocks.NewMockAutoScalingAPI(ctrl)
		ec2Mock := mocks.NewMockEC2API(ctrl)
		drainerMock := mocks.NewMockDrainer(ctrl)

		now := time.Now().UTC()
		stateSavedAt := now.Format(time.RFC3339)

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
			asMock.EXPECT().DescribeAutoScalingGroups(gomock.Any()).Return(&autoscaling.DescribeAutoScalingGroupsOutput{
				AutoScalingGroups: []*autoscaling.Group{
					{
						AutoScalingGroupName: aws.String("autoscaling-group-name"),
						AvailabilityZones: []*string{
							aws.String("ap-northeast-1a"),
							aws.String("ap-northeast-1c"),
						},
						DesiredCapacity: aws.Int64(desiredCapacity),
						Instances:       oldInstances,
						MaxSize:         aws.Int64(maxSize),
					},
				},
			}, nil),

			ec2Mock.EXPECT().DescribeInstances(gomock.Any()).Return(&ec2.DescribeInstancesOutput{
				Reservations: oldReservations,
			}, nil),

			expectLaunchNewInstances(t, asMock, oldInstances, newInstances, desiredCapacity, maxSize, stateSavedAt),
			expectTerminateInstances(t, asMock, ec2Mock, drainerMock, oldInstances, newInstances, oldReservations, newReservations, desiredCapacity, maxSize),
			expectRestoreState(t, asMock, desiredCapacity, maxSize, stateSavedAt),
		)

		group, err := capacity.NewAutoScalingGroup("autoscaling-group-name", asMock, ec2Mock)
		if err != nil {
			t.Fatal(err)
		}

		err = group.ReplaceInstances(drainerMock)
		if err != nil {
			t.Errorf("err = %#v; want nil", err)
		}
	})

	t.Run("the desired capacity is not a multiple of the number of availability zones", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		desiredCapacity := int64(5)
		maxSize := int64(8)

		asMock := mocks.NewMockAutoScalingAPI(ctrl)
		ec2Mock := mocks.NewMockEC2API(ctrl)
		drainerMock := mocks.NewMockDrainer(ctrl)

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
			asMock.EXPECT().DescribeAutoScalingGroups(gomock.Any()).Return(&autoscaling.DescribeAutoScalingGroupsOutput{
				AutoScalingGroups: []*autoscaling.Group{
					{
						AutoScalingGroupName: aws.String("autoscaling-group-name"),
						AvailabilityZones: []*string{
							aws.String("ap-northeast-1a"),
							aws.String("ap-northeast-1c"),
							aws.String("ap-northeast-1d"),
						},
						DesiredCapacity: aws.Int64(desiredCapacity),
						Instances:       oldInstances,
						MaxSize:         aws.Int64(maxSize),
					},
				},
			}, nil),

			ec2Mock.EXPECT().DescribeInstances(gomock.Any()).Return(&ec2.DescribeInstancesOutput{
				Reservations: oldReservations,
			}, nil),

			expectLaunchNewInstances(t, asMock, oldInstances, newInstances, desiredCapacity, maxSize, stateSavedAt),
			expectTerminateInstances(t, asMock, ec2Mock, drainerMock, instancesToTerminate, instancesToKeep, reservationsToTerminate, reservationsToKeep, desiredCapacity, maxSize),
			expectRestoreState(t, asMock, desiredCapacity, maxSize, stateSavedAt),
		)

		group, err := capacity.NewAutoScalingGroup("autoscaling-group-name", asMock, ec2Mock)
		if err != nil {
			t.Fatal(err)
		}

		err = group.ReplaceInstances(drainerMock)
		if err != nil {
			t.Errorf("err = %#v; want nil", err)
		}
	})

	t.Run("replacement is already finished", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		desiredCapacity := int64(6)
		maxSize := int64(8)

		asMock := mocks.NewMockAutoScalingAPI(ctrl)
		ec2Mock := mocks.NewMockEC2API(ctrl)
		drainerMock := mocks.NewMockDrainer(ctrl)

		now := time.Now().UTC()
		stateSavedAt := now.Format(time.RFC3339)

		instances := append(
			createInstances("ap-northeast-1a", int(desiredCapacity/2)),
			createInstances("ap-northeast-1c", int(desiredCapacity/2))...,
		)

		gomock.InOrder(
			asMock.EXPECT().DescribeAutoScalingGroups(gomock.Any()).Return(&autoscaling.DescribeAutoScalingGroupsOutput{
				AutoScalingGroups: []*autoscaling.Group{
					{
						AutoScalingGroupName: aws.String("autoscaling-group-name"),
						AvailabilityZones: []*string{
							aws.String("ap-northeast-1a"),
							aws.String("ap-northeast-1c"),
						},
						DesiredCapacity: aws.Int64(desiredCapacity),
						Instances:       instances,
						MaxSize:         aws.Int64(maxSize),
						Tags: []*autoscaling.TagDescription{
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

			ec2Mock.EXPECT().DescribeInstances(gomock.Any()).Return(&ec2.DescribeInstancesOutput{
				Reservations: createReservations(instances, now),
			}, nil),

			expectRestoreState(t, asMock, desiredCapacity, maxSize, stateSavedAt),
		)

		group, err := capacity.NewAutoScalingGroup("autoscaling-group-name", asMock, ec2Mock)
		if err != nil {
			t.Fatal(err)
		}

		err = group.ReplaceInstances(drainerMock)
		if err != nil {
			t.Errorf("err = %#v; want nil", err)
		}
	})

	t.Run("the process is resumed after new instances are launched", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		desiredCapacity := int64(6)
		maxSize := int64(8)

		asMock := mocks.NewMockAutoScalingAPI(ctrl)
		ec2Mock := mocks.NewMockEC2API(ctrl)
		drainerMock := mocks.NewMockDrainer(ctrl)

		now := time.Now().UTC()
		stateSavedAt := now.Format(time.RFC3339)
		tags := []*autoscaling.TagDescription{
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
			asMock.EXPECT().DescribeAutoScalingGroups(gomock.Any()).Times(2).Return(&autoscaling.DescribeAutoScalingGroupsOutput{
				AutoScalingGroups: []*autoscaling.Group{
					{
						AutoScalingGroupName: aws.String("autoscaling-group-name"),
						AvailabilityZones: []*string{
							aws.String("ap-northeast-1a"),
							aws.String("ap-northeast-1c"),
						},
						DesiredCapacity: aws.Int64(int64(len(oldInstances) + len(newInstances))),
						Instances:       append(oldInstances, newInstances...),
						MaxSize:         aws.Int64(int64(len(oldInstances) + len(newInstances))),
						Tags:            tags,
					},
				},
			}, nil),

			ec2Mock.EXPECT().DescribeInstances(gomock.Any()).Return(&ec2.DescribeInstancesOutput{
				Reservations: oldReservations,
			}, nil),

			expectTerminateInstances(t, asMock, ec2Mock, drainerMock, oldInstances, newInstances, oldReservations, newReservations, desiredCapacity, maxSize),
			expectRestoreState(t, asMock, desiredCapacity, maxSize, stateSavedAt),
		)

		group, err := capacity.NewAutoScalingGroup("autoscaling-group-name", asMock, ec2Mock)
		if err != nil {
			t.Fatal(err)
		}

		err = group.ReplaceInstances(drainerMock)
		if err != nil {
			t.Errorf("err = %#v; want nil", err)
		}
	})
}

func TestAutoScalingGroup_ReduceCapacity(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	asMock := mocks.NewMockAutoScalingAPI(ctrl)
	ec2Mock := mocks.NewMockEC2API(ctrl)
	drainerMock := mocks.NewMockDrainer(ctrl)

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
		asMock.EXPECT().DescribeAutoScalingGroups(gomock.Any()).Return(&autoscaling.DescribeAutoScalingGroupsOutput{
			AutoScalingGroups: []*autoscaling.Group{
				{
					AutoScalingGroupName: aws.String("autoscaling-group-name"),
					DesiredCapacity:      aws.Int64(int64(len(allInstances))),
					Instances:            allInstances,
					MaxSize:              aws.Int64(int64(len(allInstances))),
				},
			},
		}, nil),

		ec2Mock.EXPECT().DescribeInstances(gomock.Any()).Return(&ec2.DescribeInstancesOutput{
			Reservations: append(reservationsToTerminate, reservationsToKeep...),
		}, nil),

		drainerMock.EXPECT().Drain(gomock.Len(len(instancesToTerminate))),

		asMock.EXPECT().DetachInstances(gomock.Any()).Times(4).Do(func(input *autoscaling.DetachInstancesInput) {
			detachedInstanceIds = append(detachedInstanceIds, aws.StringValueSlice(input.InstanceIds)...)
		}),

		ec2Mock.EXPECT().TerminateInstances(gomock.Any()).Do(func(input *ec2.TerminateInstancesInput) {
			terminatedInstanceIds = append(terminatedInstanceIds, aws.StringValueSlice(input.InstanceIds)...)
		}),

		ec2Mock.EXPECT().WaitUntilInstanceTerminated(gomock.Any()),

		// Call `reload` at the end of the method
		asMock.EXPECT().DescribeAutoScalingGroups(gomock.Any()).Return(&autoscaling.DescribeAutoScalingGroupsOutput{
			AutoScalingGroups: []*autoscaling.Group{
				{
					AutoScalingGroupName: aws.String("autoscaling-group-name"),
					DesiredCapacity:      aws.Int64(int64(len(instancesToKeep))),
					Instances:            instancesToKeep,
					MaxSize:              aws.Int64(int64(len(allInstances))),
				},
			},
		}, nil),
	)

	group, err := capacity.NewAutoScalingGroup("autoscaling-group-name", asMock, ec2Mock)
	if err != nil {
		t.Fatal(err)
	}

	err = group.ReduceCapacity(int64(len(instancesToTerminate)), drainerMock)
	if err != nil {
		t.Errorf("err = %#v; want nil", err)
	}

	instanceIdsToTerminate := make([]string, len(instancesToTerminate))
	for i, instance := range instancesToTerminate {
		instanceIdsToTerminate[i] = *instance.InstanceId
	}
	if !matchSlice(detachedInstanceIds, instanceIdsToTerminate) {
		t.Errorf("detachedInstanceIds = %v; want %v", detachedInstanceIds, instanceIdsToTerminate)
	}
	if !matchSlice(terminatedInstanceIds, instanceIdsToTerminate) {
		t.Errorf("terminatedInstanceIds = %v; want %v", terminatedInstanceIds, instanceIdsToTerminate)
	}

}
