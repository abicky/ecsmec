package capacity

import (
	"context"
	"fmt"
	"log"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	autoscalingtypes "github.com/aws/aws-sdk-go-v2/service/autoscaling/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"golang.org/x/xerrors"

	"github.com/abicky/ecsmec/internal/const/autoscalingconst"
)

type AutoScalingGroup struct {
	OriginalDesiredCapacity *int32
	OriginalMaxSize         *int32
	StateSavedAt            *time.Time

	autoscalingtypes.AutoScalingGroup

	asSvc  AutoScalingAPI
	ec2Svc EC2API
	name   string
}

func NewAutoScalingGroup(name string, asSvc AutoScalingAPI, ec2Svc EC2API) (*AutoScalingGroup, error) {
	asg := AutoScalingGroup{asSvc: asSvc, ec2Svc: ec2Svc, name: name}
	if err := asg.reload(context.Background()); err != nil {
		return nil, err
	}
	return &asg, nil
}

func (asg *AutoScalingGroup) ReplaceInstances(ctx context.Context, drainer Drainer, cluster Cluster) error {
	oldInstanceIDs := make([]string, 0)
	baseTime := asg.StateSavedAt
	if baseTime == nil {
		baseTime = aws.Time(time.Now())
	}

	err := asg.fetchInstances(ctx, func(i ec2types.Instance) error {
		if i.LaunchTime.Before(*baseTime) {
			oldInstanceIDs = append(oldInstanceIDs, *i.InstanceId)
		}
		return nil
	})
	if err != nil {
		return xerrors.Errorf("failed to fetch old instance IDs: %w", err)
	}

	if err := asg.launchNewInstances(ctx, len(oldInstanceIDs)); err != nil {
		return xerrors.Errorf("failed to launch new instances: %w", err)
	}

	newInstanceCount := *asg.DesiredCapacity - *asg.OriginalDesiredCapacity
	log.Printf("Wait for all the new instances to be registered in the cluster %q\n", cluster.Name())
	if err := cluster.WaitUntilContainerInstancesRegistered(ctx, int(newInstanceCount), asg.StateSavedAt); err != nil {
		return xerrors.Errorf("failed to wait until container instances are registered: %w", err)
	}

	if err := asg.terminateInstances(ctx, newInstanceCount, drainer); err != nil {
		return xerrors.Errorf("failed to terminate instances: %w", err)
	}

	if err := asg.restoreState(ctx); err != nil {
		return xerrors.Errorf("failed to restore the auto scaling group: %w", err)
	}

	return nil
}

func (asg *AutoScalingGroup) ReduceCapacity(ctx context.Context, amount int32, drainer Drainer) error {
	return asg.terminateInstances(ctx, amount, drainer)
}

func (asg *AutoScalingGroup) reload(ctx context.Context) error {
	resp, err := asg.asSvc.DescribeAutoScalingGroups(ctx, &autoscaling.DescribeAutoScalingGroupsInput{
		AutoScalingGroupNames: []string{asg.name},
	})
	if err != nil {
		return xerrors.Errorf("failed to describe the auto scaling group: %w", err)
	}

	if len(resp.AutoScalingGroups) == 0 {
		return xerrors.Errorf("the auto scaling group \"%s\" doesn't exist", asg.name)
	}

	asg.AutoScalingGroup = resp.AutoScalingGroups[0]
	asg.OriginalDesiredCapacity = asg.DesiredCapacity
	asg.OriginalMaxSize = asg.MaxSize
	for _, t := range asg.Tags {
		switch *t.Key {
		case "ecsmec:OriginalDesiredCapacity":
			originalDesiredCapacity, err := strconv.ParseInt(*t.Value, 10, 32)
			if err != nil {
				return xerrors.Errorf("ecsmec:OriginalDesiredCapacity is invalid (%s): %w", *t.Value, err)
			}
			asg.OriginalDesiredCapacity = aws.Int32(int32(originalDesiredCapacity))
		case "ecsmec:OriginalMaxSize":
			originalMaxSize, err := strconv.ParseInt(*t.Value, 10, 32)
			if err != nil {
				return xerrors.Errorf("ecsmec:OriginalMaxSize is invalid (%s): %w", *t.Value, err)
			}
			asg.OriginalMaxSize = aws.Int32(int32(originalMaxSize))
		case "ecsmec:StateSavedAt":
			stateSavedAt, err := time.Parse(time.RFC3339, *t.Value)
			if err != nil {
				return xerrors.Errorf("ecsmec:StateSavedAt is invalid (%s): %w", *t.Value, err)
			}
			asg.StateSavedAt = &stateSavedAt
		}
	}

	return nil
}

func (asg *AutoScalingGroup) fetchInstances(ctx context.Context, callback func(ec2types.Instance) error) error {
	ids := make([]string, len(asg.Instances))
	for i, instance := range asg.Instances {
		ids[i] = *instance.InstanceId
	}

	resp, err := asg.ec2Svc.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		InstanceIds: ids,
	})
	if err != nil {
		return xerrors.Errorf("failed to describe instances: %w", err)
	}

	for _, r := range resp.Reservations {
		for _, i := range r.Instances {
			if err := callback(i); err != nil {
				return err
			}
		}
	}

	return nil
}

func (asg *AutoScalingGroup) launchNewInstances(ctx context.Context, oldInstanceCount int) error {
	if oldInstanceCount == 0 {
		return nil
	}
	requiredCount := int32(oldInstanceCount)

	if len(asg.AvailabilityZones) > 2 && *asg.OriginalDesiredCapacity%int32(len(asg.AvailabilityZones)) > 0 {
		// If there are more than two availability zones, the new desired capacity must be a multiple of the number of
		// availability zones, otherwise AZRebalance will terminate some instances unexpectedly.
		// Assume that there are following instances in each availability zone:
		//   ap-northeast-1a: 2, ap-northeast-1c: 1, ap-northeast-1d: 2
		// After increasing the desired capacity to 10, that is, launching new instances as many as the old instances,
		// the number of instances will change as below:
		//   ap-northeast-1a: 3 (old: 2, new: 1), ap-northeast-1c: 4 (old: 1, new: 3), ap-northeast-1d: 3 (old: 2, new: 1)
		// After terminating old instances, the number of instances will change as below:
		//   ap-northeast-1a: 1, ap-northeast-1c: 3, ap-northeast-1d: 1
		// AZRebalance will launch another instance in ap-northeast-1a or ap-northeast-1d and terminate one
		// in ap-northeast-1c without draining it.
		requiredCount += int32(len(asg.AvailabilityZones)) - *asg.OriginalDesiredCapacity%int32(len(asg.AvailabilityZones))
	}

	if err := asg.waitUntilInstancesInService(ctx, *asg.DesiredCapacity); err != nil {
		return xerrors.Errorf("failed to wait until %d instances are in service: %w", *asg.DesiredCapacity, err)
	}

	newDesiredCapacity := *asg.OriginalDesiredCapacity + requiredCount
	if newDesiredCapacity <= *asg.DesiredCapacity {
		return nil
	}

	newDesiredMaxSize := *asg.MaxSize
	if newDesiredCapacity > newDesiredMaxSize {
		newDesiredMaxSize = newDesiredCapacity
	}

	if err := asg.saveCurrentState(ctx); err != nil {
		return xerrors.Errorf("failed to save the current state: %w", err)
	}

	log.Printf("Update the auto scaling group \"%s\": DesirdCapacity: %d, MaxSize: %d\n",
		*asg.AutoScalingGroupName, newDesiredCapacity, newDesiredMaxSize)
	_, err := asg.asSvc.UpdateAutoScalingGroup(ctx, &autoscaling.UpdateAutoScalingGroupInput{
		AutoScalingGroupName: asg.AutoScalingGroupName,
		DesiredCapacity:      aws.Int32(newDesiredCapacity),
		MaxSize:              aws.Int32(newDesiredMaxSize),
	})
	if err != nil {
		return xerrors.Errorf("failed to update the auto scaling group: %w", err)
	}

	if err := asg.waitUntilInstancesInService(ctx, newDesiredCapacity); err != nil {
		return xerrors.Errorf("failed to wait until %d instances are in service: %w", newDesiredCapacity, err)
	}

	return asg.reload(ctx)
}

func (asg *AutoScalingGroup) terminateInstances(ctx context.Context, count int32, drainer Drainer) error {
	if count == 0 {
		return nil
	}

	// Sort instanceIDs to prevent AZRebalance from terminating instances unexpectedly
	sortedInstanceIDs, err := asg.fetchSortedInstanceIDs(ctx, count)
	if err != nil {
		return xerrors.Errorf("failed to fetch sorted instance IDs: %w", err)
	}

	if err := drainer.Drain(ctx, sortedInstanceIDs); err != nil {
		return xerrors.Errorf("failed to drain instances: %w", err)
	}

	for ids := range slices.Chunk(sortedInstanceIDs, autoscalingconst.MaxDetachableInstances) {
		log.Println("Detach instances:", ids)
		_, err := asg.asSvc.DetachInstances(ctx, &autoscaling.DetachInstancesInput{
			AutoScalingGroupName:           asg.AutoScalingGroupName,
			InstanceIds:                    ids,
			ShouldDecrementDesiredCapacity: aws.Bool(true),
		})
		if err != nil {
			return xerrors.Errorf("failed to detach instances: %w", err)
		}
	}

	log.Println("Terminate instances:", sortedInstanceIDs)
	_, err = asg.ec2Svc.TerminateInstances(ctx, &ec2.TerminateInstancesInput{
		InstanceIds: sortedInstanceIDs,
	})
	if err != nil {
		return xerrors.Errorf("failed to terminate the instances: %w", err)
	}

	waiter := ec2.NewInstanceTerminatedWaiter(asg.ec2Svc, func(o *ec2.InstanceTerminatedWaiterOptions) {
		o.MaxDelay = 15 * time.Second
	})
	err = waiter.Wait(ctx, &ec2.DescribeInstancesInput{
		InstanceIds: sortedInstanceIDs,
	}, 10*time.Minute)
	if err != nil {
		return xerrors.Errorf("failed to terminate the instances: %w", err)
	}

	return asg.reload(ctx)
}

func (asg *AutoScalingGroup) restoreState(ctx context.Context) error {
	if *asg.DesiredCapacity != *asg.OriginalDesiredCapacity {
		return xerrors.Errorf("can't restore the state unless the desired capacity is %d", *asg.OriginalDesiredCapacity)
	}

	log.Printf("Update the auto scaling group \"%s\": MaxSize: %d\n", *asg.AutoScalingGroupName, *asg.OriginalMaxSize)

	_, err := asg.asSvc.UpdateAutoScalingGroup(ctx, &autoscaling.UpdateAutoScalingGroupInput{
		AutoScalingGroupName: asg.AutoScalingGroupName,
		MaxSize:              asg.OriginalMaxSize,
	})
	if err != nil {
		return xerrors.Errorf("failed to update the auto scaling group: %w", err)
	}

	_, err = asg.asSvc.DeleteTags(ctx, &autoscaling.DeleteTagsInput{
		Tags: []autoscalingtypes.Tag{
			asg.createTag("ecsmec:OriginalDesiredCapacity", fmt.Sprint(*asg.OriginalDesiredCapacity)),
			asg.createTag("ecsmec:OriginalMaxSize", fmt.Sprint(*asg.OriginalMaxSize)),
			asg.createTag("ecsmec:StateSavedAt", fmt.Sprint(asg.StateSavedAt.Format(time.RFC3339))),
		},
	})
	if err != nil {
		return xerrors.Errorf("failed to delete tags: %w", err)
	}

	return asg.reload(ctx)
}

func (asg *AutoScalingGroup) saveCurrentState(ctx context.Context) error {
	var stateSavedAt time.Time
	if asg.StateSavedAt != nil {
		stateSavedAt = *asg.StateSavedAt
	} else {
		stateSavedAt = time.Now().UTC()
	}

	_, err := asg.asSvc.CreateOrUpdateTags(ctx, &autoscaling.CreateOrUpdateTagsInput{
		Tags: []autoscalingtypes.Tag{
			asg.createTag("ecsmec:OriginalDesiredCapacity", fmt.Sprint(*asg.OriginalDesiredCapacity)),
			asg.createTag("ecsmec:OriginalMaxSize", fmt.Sprint(*asg.OriginalMaxSize)),
			asg.createTag("ecsmec:StateSavedAt", stateSavedAt.Format(time.RFC3339)),
		},
	})
	if err != nil {
		return xerrors.Errorf("failed to create or update tags: %w", err)
	}

	return nil
}

func (asg *AutoScalingGroup) createTag(key string, value string) autoscalingtypes.Tag {
	return autoscalingtypes.Tag{
		Key:               aws.String(key),
		PropagateAtLaunch: aws.Bool(false),
		ResourceId:        asg.AutoScalingGroupName,
		ResourceType:      aws.String("auto-scaling-group"),
		Value:             aws.String(value),
	}
}

func (asg *AutoScalingGroup) waitUntilInstancesInService(ctx context.Context, capacity int32) error {
	// NOTE: autoscaling.GroupInServiceWaiter waits until there are MinSize instances with lifecycle state "InService",
	// that is, without increasing MinSize, Wait() might exists immediately.
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	timeout := 5 * time.Minute
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		resp, err := asg.asSvc.DescribeAutoScalingGroups(ctx, &autoscaling.DescribeAutoScalingGroupsInput{
			AutoScalingGroupNames: []string{*asg.AutoScalingGroupName},
		})
		if err != nil {
			return xerrors.Errorf("failed to describe the auto scaling group: %w", err)
		}

		healthyInstanceCnt := int32(0)
		for _, i := range resp.AutoScalingGroups[0].Instances {
			if i.LifecycleState == "InService" {
				healthyInstanceCnt++
				if healthyInstanceCnt == capacity {
					return nil
				}
			}
		}

		select {
		case <-ticker.C:
			continue
		case <-timer.C:
			return xerrors.Errorf("can't prepare at least %d in-service instances within %v", capacity, timeout)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (asg *AutoScalingGroup) fetchSortedInstanceIDs(ctx context.Context, count int32) ([]string, error) {
	instances := make([]ec2types.Instance, 0, *asg.DesiredCapacity)
	err := asg.fetchInstances(ctx, func(i ec2types.Instance) error {
		instances = append(instances, i)
		return nil
	})
	if err != nil {
		return nil, xerrors.Errorf("failed to fetch instances: %w", err)
	}

	sort.SliceStable(instances, func(i, j int) bool {
		return instances[i].LaunchTime.Before(*instances[j].LaunchTime)
	})

	azs := make([]string, 0)
	azToInstances := make(map[string][]ec2types.Instance)
	azToOldInstanceCount := make(map[string]int)
	for _, i := range instances {
		az := *i.Placement.AvailabilityZone
		if !slices.Contains(azs, az) {
			azs = append(azs, az)
		}
		azToInstances[az] = append(azToInstances[az], i)
		if asg.StateSavedAt != nil && i.LaunchTime.Before(*asg.StateSavedAt) {
			azToOldInstanceCount[az] += 1
		}
	}

	sort.SliceStable(azs, func(i, j int) bool {
		if len(azToInstances[azs[i]]) == len(azToInstances[azs[j]]) {
			return azToOldInstanceCount[azs[i]] > azToOldInstanceCount[azs[j]]
		} else {
			return len(azToInstances[azs[i]]) > len(azToInstances[azs[j]])
		}
	})

	sortedInstanceIDs := make([]string, 0, count)
Loop:
	for {
		for _, az := range azs {
			if int32(len(sortedInstanceIDs)) == count {
				break Loop
			}
			var i ec2types.Instance
			i, azToInstances[az] = azToInstances[az][0], azToInstances[az][1:]
			sortedInstanceIDs = append(sortedInstanceIDs, *i.InstanceId)
		}
	}

	return sortedInstanceIDs, nil
}
