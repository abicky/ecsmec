package capacity_test

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/ecs"
	"github.com/aws/aws-sdk-go/service/sqs"
	"go.uber.org/mock/gomock"

	"github.com/abicky/ecsmec/internal/capacity"
	"github.com/abicky/ecsmec/internal/testing/mocks"
)

func TestDrainer_Drain(t *testing.T) {
	t.Run("with container instances", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		ecsMock := mocks.NewMockECSAPI(ctrl)

		instances := append(createInstances("ap-northeast-1a", 2), createInstances("ap-northeast-1c", 1)...)
		instanceIDs := make([]string, len(instances))
		containerInstanceArns := make([]*string, len(instances))
		containerInstances := make([]*ecs.ContainerInstance, len(instances))
		for i, instance := range instances {
			instanceIDs[i] = *instance.InstanceId
			arn := fmt.Sprintf("arn:aws:ecs:ap-northeast-1:1234:container-instance/test/%s", *instance.InstanceId)
			containerInstanceArns[i] = aws.String(arn)
			containerInstances[i] = &ecs.ContainerInstance{
				ContainerInstanceArn: aws.String(arn),
				Ec2InstanceId:        instance.InstanceId,
			}
		}

		ecsMock.EXPECT().ListContainerInstancesPages(gomock.Any(), gomock.Any()).
			DoAndReturn(func(params *ecs.ListContainerInstancesInput, fn func(*ecs.ListContainerInstancesOutput, bool) bool) error {
				fn(&ecs.ListContainerInstancesOutput{
					ContainerInstanceArns: containerInstanceArns,
				}, true)
				return nil
			})

		ecsMock.EXPECT().DescribeContainerInstances(gomock.Any()).Return(&ecs.DescribeContainerInstancesOutput{
			ContainerInstances: containerInstances,
		}, nil)

		ecsMock.EXPECT().ListTasksPages(gomock.Any(), gomock.Any()).Times(len(instances)).
			DoAndReturn(func(params *ecs.ListTasksInput, fn func(*ecs.ListTasksOutput, bool) bool) error {
				switch *params.ContainerInstance {
				case *containerInstanceArns[0]:
					fn(&ecs.ListTasksOutput{
						TaskArns: []*string{
							aws.String("arn:aws:ecs:ap-northeast-1:123:task/test/00000000000000000000000000000000"),
							aws.String("arn:aws:ecs:ap-northeast-1:123:task/test/11111111111111111111111111111111"),
						},
					}, true)
				default:
					fn(&ecs.ListTasksOutput{TaskArns: []*string{}}, true)
				}
				return nil
			})

		ecsMock.EXPECT().DescribeTasks(gomock.Any()).Return(&ecs.DescribeTasksOutput{
			Tasks: []*ecs.Task{
				{
					Group:   aws.String("service:foo"),
					TaskArn: aws.String("arn:aws:ecs:ap-northeast-1:123:task/test/00000000000000000000000000000000"),
				},
				{
					Group:   aws.String("family:bar"),
					TaskArn: aws.String("arn:aws:ecs:ap-northeast-1:123:task/test/11111111111111111111111111111111"),
				},
			},
		}, nil)

		ecsMock.EXPECT().StopTask(gomock.Any()).
			DoAndReturn(func(input *ecs.StopTaskInput) (*ecs.StopTaskOutput, error) {
				want := "arn:aws:ecs:ap-northeast-1:123:task/test/11111111111111111111111111111111"
				if *input.Task != want {
					t.Errorf("Task = %s; want %s", *input.Task, want)
				}
				return nil, nil
			})

		ecsMock.EXPECT().UpdateContainerInstancesState(gomock.Any()).Return(&ecs.UpdateContainerInstancesStateOutput{}, nil)
		ecsMock.EXPECT().WaitUntilTasksStopped(gomock.Any()).Return(nil)
		ecsMock.EXPECT().WaitUntilServicesStable(gomock.Any()).Return(nil)

		drainer, err := capacity.NewDrainer("test", 10, ecsMock)
		if err != nil {
			t.Fatal(err)
		}

		if err := drainer.Drain(instanceIDs); err != nil {
			t.Errorf("err = %#v; want nil", err)
		}
	})

	t.Run("without container instances", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		ecsMock := mocks.NewMockECSAPI(ctrl)

		instances := append(createInstances("ap-northeast-1a", 2), createInstances("ap-northeast-1c", 1)...)
		instanceIDs := make([]string, len(instances))
		for i, instance := range instances {
			instanceIDs[i] = *instance.InstanceId
		}

		ecsMock.EXPECT().ListContainerInstancesPages(gomock.Any(), gomock.Any()).
			DoAndReturn(func(params *ecs.ListContainerInstancesInput, fn func(*ecs.ListContainerInstancesOutput, bool) bool) error {
				fn(&ecs.ListContainerInstancesOutput{
					ContainerInstanceArns: []*string{},
				}, true)
				return nil
			})

		drainer, err := capacity.NewDrainer("test", 10, ecsMock)
		if err != nil {
			t.Fatal(err)
		}

		if err := drainer.Drain(instanceIDs); err == nil {
			t.Errorf("err = nil; want non-nil")
		}
	})
}

func TestDrainer_ProcessInterruptions(t *testing.T) {
	t.Run("with container instances", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		ecsMock := mocks.NewMockECSAPI(ctrl)

		instances := append(createInstances("ap-northeast-1a", 2), createInstances("ap-northeast-1c", 1)...)
		instanceIDs := make([]string, len(instances))
		containerInstanceArns := make([]*string, len(instances))
		containerInstances := make([]*ecs.ContainerInstance, len(instances))
		expectedEntries := make([]*sqs.DeleteMessageBatchRequestEntry, len(instances))
		for i, instance := range instances {
			instanceIDs[i] = *instance.InstanceId
			arn := fmt.Sprintf("arn:aws:ecs:ap-northeast-1:1234:container-instance/test/%s", *instance.InstanceId)
			containerInstanceArns[i] = aws.String(arn)
			containerInstances[i] = &ecs.ContainerInstance{
				ContainerInstanceArn: aws.String(arn),
				Ec2InstanceId:        instance.InstanceId,
			}

			expectedEntries[i] = &sqs.DeleteMessageBatchRequestEntry{
				Id:            instance.InstanceId,
				ReceiptHandle: aws.String("receipt-handle-" + *instance.InstanceId),
			}
		}

		otherInstances := createInstances("ap-northeast-1d", 2)
		messages := make([]*sqs.Message, len(otherInstances)+len(instances))
		for i, instance := range append(otherInstances, instances...) {
			messages[i] = &sqs.Message{
				Body:          aws.String(fmt.Sprintf("{\"detail\":{\"instance-id\":\"%s\"}}", *instance.InstanceId)),
				ReceiptHandle: aws.String("receipt-handle-" + *instance.InstanceId),
			}
		}

		ecsMock.EXPECT().ListContainerInstancesPages(gomock.Any(), gomock.Any()).
			DoAndReturn(func(params *ecs.ListContainerInstancesInput, fn func(*ecs.ListContainerInstancesOutput, bool) bool) error {
				fn(&ecs.ListContainerInstancesOutput{
					ContainerInstanceArns: containerInstanceArns,
				}, true)
				return nil
			})

		ecsMock.EXPECT().DescribeContainerInstances(gomock.Any()).Return(&ecs.DescribeContainerInstancesOutput{
			ContainerInstances: containerInstances,
		}, nil)

		ecsMock.EXPECT().ListTasksPages(gomock.Any(), gomock.Any()).Times(len(instances)).
			DoAndReturn(func(params *ecs.ListTasksInput, fn func(*ecs.ListTasksOutput, bool) bool) error {
				switch *params.ContainerInstance {
				case *containerInstanceArns[0]:
					fn(&ecs.ListTasksOutput{
						TaskArns: []*string{
							aws.String("arn:aws:ecs:ap-northeast-1:123:task/test/00000000000000000000000000000000"),
							aws.String("arn:aws:ecs:ap-northeast-1:123:task/test/11111111111111111111111111111111"),
						},
					}, true)
				default:
					fn(&ecs.ListTasksOutput{TaskArns: []*string{}}, true)
				}
				return nil
			})

		ecsMock.EXPECT().DescribeTasks(gomock.Any()).Return(&ecs.DescribeTasksOutput{
			Tasks: []*ecs.Task{
				{
					Group:   aws.String("service:foo"),
					TaskArn: aws.String("arn:aws:ecs:ap-northeast-1:123:task/test/00000000000000000000000000000000"),
				},
				{
					Group:   aws.String("family:bar"),
					TaskArn: aws.String("arn:aws:ecs:ap-northeast-1:123:task/test/11111111111111111111111111111111"),
				},
			},
		}, nil)

		ecsMock.EXPECT().StopTask(gomock.Any()).
			DoAndReturn(func(input *ecs.StopTaskInput) (*ecs.StopTaskOutput, error) {
				want := "arn:aws:ecs:ap-northeast-1:123:task/test/11111111111111111111111111111111"
				if *input.Task != want {
					t.Errorf("Task = %s; want %s", *input.Task, want)
				}
				return nil, nil
			})

		ecsMock.EXPECT().UpdateContainerInstancesState(gomock.Any()).Return(&ecs.UpdateContainerInstancesStateOutput{}, nil)

		drainer, err := capacity.NewDrainer("test", 10, ecsMock)
		if err != nil {
			t.Fatal(err)
		}

		entries, err := drainer.ProcessInterruptions(messages)
		if err != nil {
			t.Errorf("err = %#v; want nil", err)
		}

		if !reflect.DeepEqual(entries, expectedEntries) {
			t.Errorf("entries = %#v; want %#v", entries, expectedEntries)
		}
	})

	t.Run("without container instances", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		ecsMock := mocks.NewMockECSAPI(ctrl)

		instances := append(createInstances("ap-northeast-1a", 2), createInstances("ap-northeast-1c", 1)...)
		messages := make([]*sqs.Message, len(instances))
		for i, instance := range instances {
			messages[i] = &sqs.Message{
				Body:          aws.String(fmt.Sprintf("{\"detail\":{\"instance-id\":\"%s\"}}", *instance.InstanceId)),
				ReceiptHandle: aws.String("receipt-handle-" + *instance.InstanceId),
			}
		}

		ecsMock.EXPECT().ListContainerInstancesPages(gomock.Any(), gomock.Any()).
			DoAndReturn(func(params *ecs.ListContainerInstancesInput, fn func(*ecs.ListContainerInstancesOutput, bool) bool) error {
				fn(&ecs.ListContainerInstancesOutput{
					ContainerInstanceArns: []*string{},
				}, true)
				return nil
			})

		drainer, err := capacity.NewDrainer("test", 10, ecsMock)
		if err != nil {
			t.Fatal(err)
		}

		entries, err := drainer.ProcessInterruptions(messages)
		if err != nil {
			t.Errorf("err = nil; want non-nil")
		}
		if len(entries) > 0 {
			t.Errorf("len(entries) = %d; want %d", len(entries), 0)
		}
	})

	t.Run("with empty messages", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		ecsMock := mocks.NewMockECSAPI(ctrl)

		drainer, err := capacity.NewDrainer("test", 10, ecsMock)
		if err != nil {
			t.Fatal(err)
		}

		_, err = drainer.ProcessInterruptions([]*sqs.Message{})
		if err != nil {
			t.Errorf("err = nil; want non-nil")
		}
	})
}
