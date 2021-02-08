package service_test

import (
	"reflect"
	"testing"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/ecs"

	"github.com/abicky/ecsmec/internal/service"
)

func TestNewDefinitionFromExistingService(t *testing.T) {
	tests := []struct {
		name string
		s    *ecs.Service
		want *service.Definition
	}{
		{
			name: "default",
			s: &ecs.Service{
				ClusterArn:    aws.String("arn:aws:ecs:ap-northeast-1:123456789:cluster/default"),
				PropagateTags: aws.String("NONE"),
				RoleArn:       aws.String("arn:aws:iam::123456789:role/aws-service-role/ecs.amazonaws.com/AWSServiceRoleForECS"),
			},
			want: &service.Definition{
				Cluster:       aws.String("arn:aws:ecs:ap-northeast-1:123456789:cluster/default"),
				PropagateTags: nil,
				Role:          nil,
			},
		},
		{
			name: "when the service linked role has a suffix",
			s: &ecs.Service{
				ClusterArn:    aws.String("arn:aws:ecs:ap-northeast-1:123456789:cluster/default"),
				PropagateTags: aws.String("NONE"),
				RoleArn:       aws.String("arn:aws:iam::123456789:role/aws-service-role/ecs.amazonaws.com/AWSServiceRoleForECS2"),
			},
			want: &service.Definition{
				Cluster:       aws.String("arn:aws:ecs:ap-northeast-1:123456789:cluster/default"),
				PropagateTags: nil,
				Role:          nil,
			},
		},
		{
			name: "when PropagateTags is specified",
			s: &ecs.Service{
				ClusterArn:    aws.String("arn:aws:ecs:ap-northeast-1:123456789:cluster/default"),
				PropagateTags: aws.String("TASK_DEFINITION"),
				RoleArn:       aws.String("arn:aws:iam::123456789:role/aws-service-role/ecs.amazonaws.com/AWSServiceRoleForECS"),
			},
			want: &service.Definition{
				Cluster:       aws.String("arn:aws:ecs:ap-northeast-1:123456789:cluster/default"),
				PropagateTags: aws.String("TASK_DEFINITION"),
				Role:          nil,
			},
		},
		{
			name: "when Role is specified",
			s: &ecs.Service{
				ClusterArn:    aws.String("arn:aws:ecs:ap-northeast-1:123456789:cluster/default"),
				PropagateTags: aws.String("NONE"),
				RoleArn:       aws.String("arn:aws:iam::123456789:role/CustomRole"),
			},
			want: &service.Definition{
				Cluster:       aws.String("arn:aws:ecs:ap-northeast-1:123456789:cluster/default"),
				PropagateTags: nil,
				Role:          aws.String("arn:aws:iam::123456789:role/CustomRole"),
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
