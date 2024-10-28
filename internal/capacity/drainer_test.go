package capacity_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"go.uber.org/mock/gomock"

	"github.com/abicky/ecsmec/internal/capacity"
	"github.com/abicky/ecsmec/internal/testing/capacitymock"
)

func TestDrainer_Drain(t *testing.T) {
	t.Run("with container instances", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		ctx := context.Background()

		ecsMock := capacitymock.NewMockECSAPI(ctrl)

		instances := append(createInstances("ap-northeast-1a", 2), createInstances("ap-northeast-1c", 1)...)
		instanceIDs := make([]string, len(instances))
		containerInstanceArns := make([]string, len(instances))
		containerInstances := make([]ecstypes.ContainerInstance, len(instances))
		for i, instance := range instances {
			instanceIDs[i] = *instance.InstanceId
			arn := fmt.Sprintf("arn:aws:ecs:ap-northeast-1:1234:container-instance/test/%s", *instance.InstanceId)
			containerInstanceArns[i] = arn
			containerInstances[i] = ecstypes.ContainerInstance{
				ContainerInstanceArn: aws.String(arn),
				Ec2InstanceId:        instance.InstanceId,
			}
		}

		// For ListTasksPaginator
		ecsMock.EXPECT().ListContainerInstances(ctx, gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, params *ecs.ListContainerInstancesInput, _ ...func(*ecs.Options)) (*ecs.ListContainerInstancesOutput, error) {
				return &ecs.ListContainerInstancesOutput{
					ContainerInstanceArns: containerInstanceArns,
				}, nil
			})

		ecsMock.EXPECT().DescribeContainerInstances(ctx, gomock.Any()).Return(&ecs.DescribeContainerInstancesOutput{
			ContainerInstances: containerInstances,
		}, nil)

		ecsMock.EXPECT().ListTasks(ctx, gomock.Any(), gomock.Any()).Times(len(instances)).
			DoAndReturn(func(_ context.Context, params *ecs.ListTasksInput, _ ...func(*ecs.Options)) (*ecs.ListTasksOutput, error) {
				output := &ecs.ListTasksOutput{TaskArns: []string{}}
				if *params.ContainerInstance == containerInstanceArns[0] {
					output.TaskArns = []string{
						"arn:aws:ecs:ap-northeast-1:123:task/test/00000000000000000000000000000000",
						"arn:aws:ecs:ap-northeast-1:123:task/test/11111111111111111111111111111111",
					}
				}
				return output, nil
			})

		ecsMock.EXPECT().DescribeTasks(ctx, gomock.Any()).Return(&ecs.DescribeTasksOutput{
			Tasks: []ecstypes.Task{
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

		ecsMock.EXPECT().StopTask(ctx, gomock.Any()).
			DoAndReturn(func(_ context.Context, input *ecs.StopTaskInput, _ ...func(options *ecs.Options)) (*ecs.StopTaskOutput, error) {
				want := "arn:aws:ecs:ap-northeast-1:123:task/test/11111111111111111111111111111111"
				if *input.Task != want {
					t.Errorf("Task = %s; want %s", *input.Task, want)
				}
				return nil, nil
			})

		ecsMock.EXPECT().UpdateContainerInstancesState(ctx, gomock.Any()).Return(&ecs.UpdateContainerInstancesStateOutput{}, nil)
		// For ecs.TasksStoppedWaiter
		ecsMock.EXPECT().DescribeTasks(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, input *ecs.DescribeTasksInput, _ ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error) {
			return &ecs.DescribeTasksOutput{
				Tasks: []ecstypes.Task{
					{
						LastStatus: aws.String("STOPPED"),
					},
				},
			}, nil
		})
		// For ecs.ServicesStableWaiter
		ecsMock.EXPECT().DescribeServices(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, input *ecs.DescribeServicesInput, _ ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error) {
			return &ecs.DescribeServicesOutput{
				Services: []ecstypes.Service{
					{
						Deployments:  make([]ecstypes.Deployment, 1),
						DesiredCount: 0,
						RunningCount: 0,
						Status:       aws.String("ACTIVE"),
					},
				},
			}, nil
		})

		drainer, err := capacity.NewDrainer("test", 10, ecsMock)
		if err != nil {
			t.Fatal(err)
		}

		if err := drainer.Drain(context.Background(), instanceIDs); err != nil {
			t.Errorf("err = %#v; want nil", err)
		}
	})

	t.Run("without container instances", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		ctx := context.Background()

		ecsMock := capacitymock.NewMockECSAPI(ctrl)

		instances := append(createInstances("ap-northeast-1a", 2), createInstances("ap-northeast-1c", 1)...)
		instanceIDs := make([]string, len(instances))
		for i, instance := range instances {
			instanceIDs[i] = *instance.InstanceId
		}

		// For ListContainerInstancesPaginator
		ecsMock.EXPECT().ListContainerInstances(ctx, gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, params *ecs.ListContainerInstancesInput, _ ...func(options *ecs.Options)) (*ecs.ListContainerInstancesOutput, error) {
				return &ecs.ListContainerInstancesOutput{
					ContainerInstanceArns: []string{},
				}, nil
			})

		drainer, err := capacity.NewDrainer("test", 10, ecsMock)
		if err != nil {
			t.Fatal(err)
		}

		if err := drainer.Drain(context.Background(), instanceIDs); err == nil {
			t.Errorf("err = nil; want non-nil")
		}
	})
}

func TestDrainer_ProcessInterruptions(t *testing.T) {
	t.Run("with container instances", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		ctx := context.Background()

		ecsMock := capacitymock.NewMockECSAPI(ctrl)

		instances := append(createInstances("ap-northeast-1a", 2), createInstances("ap-northeast-1c", 1)...)
		instanceIDs := make([]string, len(instances))
		containerInstanceArns := make([]string, len(instances))
		containerInstances := make([]ecstypes.ContainerInstance, len(instances))
		expectedEntries := make([]sqstypes.DeleteMessageBatchRequestEntry, len(instances))
		for i, instance := range instances {
			instanceIDs[i] = *instance.InstanceId
			arn := fmt.Sprintf("arn:aws:ecs:ap-northeast-1:1234:container-instance/test/%s", *instance.InstanceId)
			containerInstanceArns[i] = arn
			containerInstances[i] = ecstypes.ContainerInstance{
				ContainerInstanceArn: aws.String(arn),
				Ec2InstanceId:        instance.InstanceId,
			}

			expectedEntries[i] = sqstypes.DeleteMessageBatchRequestEntry{
				Id:            instance.InstanceId,
				ReceiptHandle: aws.String("receipt-handle-" + *instance.InstanceId),
			}
		}

		otherInstances := createInstances("ap-northeast-1d", 2)
		messages := make([]sqstypes.Message, len(otherInstances)+len(instances))
		for i, instance := range append(otherInstances, instances...) {
			messages[i] = sqstypes.Message{
				Body:          aws.String(fmt.Sprintf("{\"detail\":{\"instance-id\":\"%s\"}}", *instance.InstanceId)),
				ReceiptHandle: aws.String("receipt-handle-" + *instance.InstanceId),
			}
		}

		// For ListContainerInstancesPaginator
		ecsMock.EXPECT().ListContainerInstances(ctx, gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, params *ecs.ListContainerInstancesInput, _ ...func(options *ecs.Options)) (*ecs.ListContainerInstancesOutput, error) {
				return &ecs.ListContainerInstancesOutput{
					ContainerInstanceArns: containerInstanceArns,
				}, nil
			})

		ecsMock.EXPECT().DescribeContainerInstances(ctx, gomock.Any()).Return(&ecs.DescribeContainerInstancesOutput{
			ContainerInstances: containerInstances,
		}, nil)

		// For ListTasksPaginator
		ecsMock.EXPECT().ListTasks(ctx, gomock.Any(), gomock.Any()).Times(len(instances)).
			DoAndReturn(func(_ context.Context, params *ecs.ListTasksInput, _ ...func(options *ecs.Options)) (*ecs.ListTasksOutput, error) {
				output := &ecs.ListTasksOutput{TaskArns: []string{}}
				if *params.ContainerInstance == containerInstanceArns[0] {
					output.TaskArns = []string{
						"arn:aws:ecs:ap-northeast-1:123:task/test/00000000000000000000000000000000",
						"arn:aws:ecs:ap-northeast-1:123:task/test/11111111111111111111111111111111",
					}
				}
				return output, nil
			})

		ecsMock.EXPECT().DescribeTasks(ctx, gomock.Any()).Return(&ecs.DescribeTasksOutput{
			Tasks: []ecstypes.Task{
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

		ecsMock.EXPECT().StopTask(ctx, gomock.Any()).
			DoAndReturn(func(_ context.Context, input *ecs.StopTaskInput, _ ...func(*ecs.Options)) (*ecs.StopTaskOutput, error) {
				want := "arn:aws:ecs:ap-northeast-1:123:task/test/11111111111111111111111111111111"
				if *input.Task != want {
					t.Errorf("Task = %s; want %s", *input.Task, want)
				}
				return nil, nil
			})

		ecsMock.EXPECT().UpdateContainerInstancesState(ctx, gomock.Any()).Return(&ecs.UpdateContainerInstancesStateOutput{}, nil)

		drainer, err := capacity.NewDrainer("test", 10, ecsMock)
		if err != nil {
			t.Fatal(err)
		}

		entries, err := drainer.ProcessInterruptions(context.Background(), messages)
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

		ctx := context.Background()

		ecsMock := capacitymock.NewMockECSAPI(ctrl)

		instances := append(createInstances("ap-northeast-1a", 2), createInstances("ap-northeast-1c", 1)...)
		messages := make([]sqstypes.Message, len(instances))
		for i, instance := range instances {
			messages[i] = sqstypes.Message{
				Body:          aws.String(fmt.Sprintf("{\"detail\":{\"instance-id\":\"%s\"}}", *instance.InstanceId)),
				ReceiptHandle: aws.String("receipt-handle-" + *instance.InstanceId),
			}
		}

		// For ListContainerInstancesPaginator
		ecsMock.EXPECT().ListContainerInstances(ctx, gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, params *ecs.ListContainerInstancesInput, _ ...func(options *ecs.Options)) (*ecs.ListContainerInstancesOutput, error) {
				return &ecs.ListContainerInstancesOutput{
					ContainerInstanceArns: []string{},
				}, nil
			})

		drainer, err := capacity.NewDrainer("test", 10, ecsMock)
		if err != nil {
			t.Fatal(err)
		}

		entries, err := drainer.ProcessInterruptions(ctx, messages)
		if err != nil {
			t.Errorf("err = %#v; want nil", err)
		}
		if len(entries) > 0 {
			t.Errorf("len(entries) = %d; want %d", len(entries), 0)
		}
	})

	t.Run("with empty messages", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		ecsMock := capacitymock.NewMockECSAPI(ctrl)

		drainer, err := capacity.NewDrainer("test", 10, ecsMock)
		if err != nil {
			t.Fatal(err)
		}

		_, err = drainer.ProcessInterruptions(context.Background(), []sqstypes.Message{})
		if err != nil {
			t.Errorf("err = %#v; want nil", err)
		}
	})
}
