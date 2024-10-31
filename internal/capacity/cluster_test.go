package capacity

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/abicky/ecsmec/internal/testing/capacitymock"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"go.uber.org/mock/gomock"
)

func TestCluster_WaitUntilContainerInstancesRegistered(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()

	ecsMock := capacitymock.NewMockECSAPI(ctrl)

	ecsMock.EXPECT().ListContainerInstances(ctx, gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, params *ecs.ListContainerInstancesInput, _ ...func(options *ecs.Options)) (*ecs.ListContainerInstancesOutput, error) {
			return &ecs.ListContainerInstancesOutput{
				ContainerInstanceArns: []string{
					fmt.Sprintf("arn:aws:ecs:ap-northeast-1:1234:container-instance/test/xxxxxxxxxx"),
				},
			}, nil
		})

	cluster := NewCluster("cluster", ecsMock)
	now := time.Now()
	if err := cluster.WaitUntilContainerInstancesRegistered(ctx, 1, &now); err != nil {
		t.Errorf("err = %#v; want nil", err)
	}
}
