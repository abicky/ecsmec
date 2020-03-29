# ecsmec

![](https://github.com/abicky/ecsmec/workflows/main/badge.svg?branch=master)

`ecsmec` is a CLI tool for Amazon ECS that provides some commands to execute bothersome operations.
For example, if you manage your ECS clusters with a auto scaling group and want to replace all the container instances with new ones, you have to launch new instances, drain old instances, and so on. What a pain!
This tool enables you to do such operations easily.

## Usage

### replace-auto-scaling-group-instances

```
$ ecsmec replace-auto-scaling-group-instances --help
This command replaces container instances that belong to the specified
auto scaling group and are launched before the time when this command
launches new ones.

Usage:
  ecsmec replace-auto-scaling-group-instances [flags]

Flags:
      --auto-scaling-group-name GROUP   The name of the target GROUP (required)
      --batch-size int                  The number of instances drained at a once (default 100)
      --cluster CLUSTER                 The name of the target CLUSTER (default "default")
  -h, --help                            help for replace-auto-scaling-group-instances

Global Flags:
      --profile string   An AWS profile name in your credential file
      --region string    The AWS region
```

You can resume the operations by executing the same command until the replacement is complete. `ecsmec` temporarily adds some tags starting with the prefix "ecsmec:" to the auto scaling group so that the command resumes the operations.

This command does the following operations to replace container instances:

1. Launch new instances
1. Drain the old container instances and stop tasks that are running on the instances and don't belong to a service
    - See the [AWS document](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/container-instance-draining.html) for more details on container instance draining
1. Detach the old instances from the auto scaling group
1. Terminate the old instances

You need the following permissions to execute the command:

```
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "autoscaling:CreateOrUpdateTags",
        "autoscaling:DeleteTags",
        "autoscaling:DetachInstances",
        "autoscaling:UpdateAutoScalingGroup"
      ],
      "Resource": "arn:aws:autoscaling:<region>:<account-id>:autoScalingGroup:*:autoScalingGroupName/<group>"
    },
    {
      "Effect": "Allow",
      "Action": [
        "autoscaling:DescribeAutoScalingGroups",
        "ec2:DescribeInstances",
        "ec2:TerminateInstances"
      ],
      "Resource": "*"
    },
    {
      "Effect": "Allow",
      "Action": [
        "ecs:ListContainerInstances"
      ],
      "Resource": [
        "arn:aws:ecs:<region>:<account-id>:cluster/<cluster>"
      ]
    },
    {
      "Effect": "Allow",
      "Action": [
        "ecs:DescribeContainerInstances",
        "ecs:ListTasks",
        "ecs:UpdateContainerInstancesState"
      ],
      "Resource": [
        "arn:aws:ecs:<region>:<account-id>:container-instance/<cluster>/*"
      ]
    },
    {
      "Effect": "Allow",
      "Action": [
        "ecs:DescribeTasks",
        "ecs:StopTask"
      ],
      "Resource": [
        "arn:aws:ecs:<region>:<account-id>:task/<cluster>/*"
      ]
    },
    {
      "Effect": "Allow",
      "Action": [
        "ecs:DescribeServices"
      ],
      "Resource": [
        "arn:aws:ecs:<region>:<account-id>:service/<cluster>/*"
      ]
    }
  ]
}
```


## Author

Takeshi Arabiki ([@abicky](http://github.com/abicky))
