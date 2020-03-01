package mocks

//go:generate mockgen -package mocks -destination autoscaling.go github.com/aws/aws-sdk-go/service/autoscaling/autoscalingiface AutoScalingAPI
//go:generate mockgen -package mocks -destination ec2.go github.com/aws/aws-sdk-go/service/ec2/ec2iface EC2API
//go:generate mockgen -package mocks -destination ecs.go github.com/aws/aws-sdk-go/service/ecs/ecsiface ECSAPI
//go:generate mockgen -package mocks -destination capacity.go github.com/abicky/ecsmec/internal/capacity Drainer
