# ecsmec

![](https://github.com/abicky/ecsmec/workflows/main/badge.svg?branch=master)

`ecsmec` is a CLI tool for Amazon ECS that provides some commands to execute bothersome operations.
For example, if you manage your ECS clusters with a auto scaling group and want to replace all the container instances with new ones, you have to launch new instances, drain old instances, and so on. What a pain!
This tool enables you to do such operations easily.

## Installation

### Install pre-compiled binary

Download the binary archive from the [releases page](https://github.com/abicky/ecsmec/releases), unpack it, and move the executable "ecsmec" to a directory in your path (e.g. `/usr/local/bin`).

For example, you can install the latest binary on a Mac with Apple silicon by the following commands:

```sh
curl -LO https://github.com/abicky/ecsmec/releases/latest/download/ecsmec_darwin_arm64.tar.gz
tar xvf ecsmec_darwin_arm64.tar.gz
mv ecsmec_darwin_arm64/ecsmec /usr/local/bin/
```

If you download the archive via a browser on macOS Catalina or later, you may receive the message "“ecsmec” cannot be opened because the developer cannot be verified."
In such a case, you need to delete the attribute "com.apple.quarantine" like below:

```sh
xattr -d com.apple.quarantine /path/to/ecsmec
```

### Install with Homebrew (macOS or Linux)

```sh
brew install abicky/tools/ecsmec
```

### Install from source

```sh
go get -u github.com:abicky/ecsmec
```

or

```sh
git clone https://github.com:abicky/ecsmec
cd ecsmec
make install
```


## Usage

### recreate-service

```console
$ ecsmec recreate-service --help
This command creates a new service from the specified service with overrides,
and after the new service becomes stable, it deletes the old one.
Therefore, as necessary, you have to increase the capacity of the cluster the
service belongs to manually so that it has enough capacity for the new service
to place its tasks.

Usage:
  ecsmec recreate-service [flags]

Examples:
  You can change the placement strategy of the service "test" in the default cluster
  by the following command:

    ecsmec recreate-service --service test --overrides '{
      "PlacementStrategy": [
        { "Field": "attribute:ecs.availability-zone", "Type": "spread" },
        { "Field": "CPU", "Type": "binpack" }
      ]
    }'

  In the same way, you can change the name of the service "test" in the default
  cluster like below:

    ecsmec recreate-service --service test --overrides '{
      "ServiceName": "new-name"
    }'


Flags:
      --cluster CLUSTER   The name of the target CLUSTER (default "default")
  -h, --help              help for recreate-service
      --overrides JSON    An JSON to override some fields of the new service (default "{}")
      --service SERVICE   The name of the target SERVICE (required)

Global Flags:
      --profile string   An AWS profile name in your credential file
      --region string    The AWS region
```

The option "overrides" is in the same format as the [CreateService API](https://docs.aws.amazon.com/AmazonECS/latest/APIReference/API_CreateService.html) parameter, except that the first letter of each field is uppercase.

This command does the following operations to recreate the specified service:

1. Create a temporal service from the service with overrides
1. Delete the old service
1. Create a new service from the temporal service
1. Delete the temporal service

If the service name is overridden, the operations change as follow:

1. Create a new service from the service with overrides
1. Delete the old service


You need the following permissions to execute the command:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "ecs:ListTasks"
      ],
      "Resource": "*"
    },
    {
      "Effect": "Allow",
      "Action": [
        "ecs:DescribeTasks"
      ],
      "Resource": [
        "arn:aws:ecs:<region>:<account-id>:task/<cluster>/*"
      ]
    },
    {
      "Effect": "Allow",
      "Action": [
        "ecs:CreateService",
        "ecs:DeleteService",
        "ecs:DescribeServices",
        "ecs:UpdateService"
      ],
      "Resource": [
        "arn:aws:ecs:<region>:<account-id>:service/<cluster>/*"
      ],
    },
    {
      "Effect": "Allow",
      "Action": [
        "iam:PassRole"
      ],
      "Resource": [
        "arn:aws:iam::<account-id>:role/<role_for_volume_configurations>"
      ]
    }
  ]
}
```

### reduce-cluster-capacity

```console
$ ecsmec reduce-cluster-capacity --help
This command reduces the capacity of the specified cluster safely
that belong to the auto scaling group or spot fleet request.

Usage:
  ecsmec reduce-cluster-capacity [flags]

Flags:
      --amount int                      The amount of the capacity to reduce (required)
      --auto-scaling-group-name GROUP   The name of the target GROUP
      --cluster CLUSTER                 The name of the target CLUSTER (default "default")
  -h, --help                            help for reduce-cluster-capacity
      --spot-fleet-request-id REQUEST   The ID of the target REQUEST

Global Flags:
      --profile string   An AWS profile name in your credential file
      --region string    The AWS region
```

This command does the following operations if `--auto-scaling-group-name` is specified:

1. Drain container instances and stop tasks that are running on the instances and don't belong to a service
    - See the [AWS document](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/container-instance-draining.html) for more details on container instance draining
1. Detach the instances from the auto scaling group
1. Terminate the instances

and does the following operations if `--spot-fleet-request-id` is specified:

1. Create a SQS queue to receive interruption warnings
1. Reduce the capacity of the spot fleet request
1. Poll the SQS queue, and then drain container instances and stop tasks that are running on the instances and don't belong to a service
    - You might think this operation is not necessary if [ECS_ENABLE_SPOT_INSTANCE_DRAINING](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/container-instance-spot.html#spot-instance-draining) is set to true, but draining doesn't stop tasks that don't belong to a service.
1. Delete the SQS queue

You need the following permissions to execute the command:

For a auto scaling group:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "autoscaling:DetachInstances"
      ],
      "Resource": "arn:aws:autoscaling:<region>:<account>:autoScalingGroup:*:autoScalingGroupName/<group>"
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
        "arn:aws:ecs:<region>:<account>:cluster/<cluster>"
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
        "arn:aws:ecs:<region>:<account>:container-instance/<cluster>/*"
      ]
    },
    {
      "Effect": "Allow",
      "Action": [
        "ecs:DescribeTasks",
        "ecs:StopTask"
      ],
      "Resource": [
        "arn:aws:ecs:<region>:<account>:task/<cluster>/*"
      ]
    },
    {
      "Effect": "Allow",
      "Action": [
        "ecs:DescribeServices"
      ],
      "Resource": [
        "arn:aws:ecs:<region>:<account>:service/<cluster>/*"
      ]
    }
  ]
}
```

For a spot fleet request:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "ec2:DescribeSpotFleetRequests",
        "ec2:ModifySpotFleetRequest"
      ],
      "Resource": "*"
    },
    {
      "Effect": "Allow",
      "Action": [
        "ecs:ListContainerInstances"
      ],
      "Resource": [
        "arn:aws:ecs:<region>:<account>:cluster/<cluster>"
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
        "arn:aws:ecs:<region>:<account>:container-instance/<cluster>/*"
      ]
    },
    {
      "Effect": "Allow",
      "Action": [
        "ecs:DescribeTasks",
        "ecs:StopTask"
      ],
      "Resource": [
        "arn:aws:ecs:<region>:<account>:task/<cluster>/*"
      ]
    },
    {
      "Effect": "Allow",
      "Action": [
        "events:DeleteRule",
        "events:PutRule",
        "events:PutTargets",
        "events:RemoveTargets"
      ],
      "Resource": [
        "arn:aws:events:<region>:<account>:rule/ecsmec-forward-ec2-spot-instance-interruption-warnings"
      ]
    },
    {
      "Effect": "Allow",
      "Action": [
        "sqs:CreateQueue",
        "sqs:DeleteMessage",
        "sqs:DeleteMessageBatch",
        "sqs:DeleteQueue",
        "sqs:GetQueueAttributes",
        "sqs:ReceiveMessage",
        "sqs:SetQueueAttributes"
      ],
      "Resource": [
        "arn:aws:sqs:<region>:<account>:ecsmec-ec2-spot-instance-interruption-warnings"
      ]
    }
  ]
}
```

### replace-auto-scaling-group-instances

```console
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

```json
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

### terminate-spot-fleet-instances

```console
$ ecsmec terminate-spot-fleet-instances --help
This command terminates all the container instances safely that belong
to the specified spot fleet request with state "cancelled".

Usage:
  ecsmec terminate-spot-fleet-instances [flags]

Flags:
      --batch-size int                  The number of instances drained at a once (default 100)
      --cluster CLUSTER                 The name of the target CLUSTER (default "default")
  -h, --help                            help for terminate-spot-fleet-instances
      --spot-fleet-request-id REQUEST   The ID of the target REQUEST (required)

Global Flags:
      --profile string   An AWS profile name in your credential file
      --region string    The AWS region
```

This command does the following operations to terminate container instances:

1. Drain container instances and stop tasks that are running on the instances and don't belong to a service
    - See the [AWS document](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/container-instance-draining.html) for more details on container instance draining
1. Terminate the instances

You need the following permissions to execute the command:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "ec2:DescribeInstances",
        "ec2:DescribeSpotFleetInstances",
        "ec2:DescribeSpotFleetRequests",
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
