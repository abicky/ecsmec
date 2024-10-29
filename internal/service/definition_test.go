package service_test

import (
	"reflect"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/abicky/ecsmec/internal/service"
)

func TestNewDefinitionFromExistingService(t *testing.T) {
	tests := []struct {
		name string
		s    ecstypes.Service
		want *service.Definition
	}{
		{
			name: "default",
			s: ecstypes.Service{
				ClusterArn:    aws.String("arn:aws:ecs:ap-northeast-1:123456789:cluster/default"),
				Deployments:   make([]ecstypes.Deployment, 1),
				DesiredCount:  1,
				PropagateTags: ecstypes.PropagateTagsNone,
				RoleArn:       aws.String("arn:aws:iam::123456789:role/aws-service-role/ecs.amazonaws.com/AWSServiceRoleForECS"),
			},
			want: &service.Definition{
				Cluster:      aws.String("arn:aws:ecs:ap-northeast-1:123456789:cluster/default"),
				DesiredCount: aws.Int32(1),
				Role:         nil,
			},
		},
		{
			name: "when the service linked role has a suffix",
			s: ecstypes.Service{
				ClusterArn:    aws.String("arn:aws:ecs:ap-northeast-1:123456789:cluster/default"),
				Deployments:   make([]ecstypes.Deployment, 1),
				DesiredCount:  1,
				PropagateTags: ecstypes.PropagateTagsNone,
				RoleArn:       aws.String("arn:aws:iam::123456789:role/aws-service-role/ecs.amazonaws.com/AWSServiceRoleForECS2"),
			},
			want: &service.Definition{
				Cluster:      aws.String("arn:aws:ecs:ap-northeast-1:123456789:cluster/default"),
				DesiredCount: aws.Int32(1),
				Role:         nil,
			},
		},
		{
			name: "when PropagateTags is specified",
			s: ecstypes.Service{
				ClusterArn:    aws.String("arn:aws:ecs:ap-northeast-1:123456789:cluster/default"),
				Deployments:   make([]ecstypes.Deployment, 1),
				DesiredCount:  1,
				PropagateTags: ecstypes.PropagateTagsTaskDefinition,
				RoleArn:       aws.String("arn:aws:iam::123456789:role/aws-service-role/ecs.amazonaws.com/AWSServiceRoleForECS"),
			},
			want: &service.Definition{
				Cluster:       aws.String("arn:aws:ecs:ap-northeast-1:123456789:cluster/default"),
				DesiredCount:  aws.Int32(1),
				PropagateTags: ecstypes.PropagateTagsTaskDefinition,
				Role:          nil,
			},
		},
		{
			name: "when Role is specified",
			s: ecstypes.Service{
				ClusterArn:    aws.String("arn:aws:ecs:ap-northeast-1:123456789:cluster/default"),
				Deployments:   make([]ecstypes.Deployment, 1),
				DesiredCount:  1,
				PropagateTags: ecstypes.PropagateTagsNone,
				RoleArn:       aws.String("arn:aws:iam::123456789:role/CustomRole"),
			},
			want: &service.Definition{
				Cluster:      aws.String("arn:aws:ecs:ap-northeast-1:123456789:cluster/default"),
				DesiredCount: aws.Int32(1),
				Role:         aws.String("arn:aws:iam::123456789:role/CustomRole"),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := service.NewDefinitionFromExistingService(tt.s); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("NewDefinitionFromExistingService() = %v; want %v", got, tt.want)
			}
		})
	}
}
