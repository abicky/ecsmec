package capacity

import (
	"fmt"
	"log"
	"sort"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/autoscaling"
	"github.com/aws/aws-sdk-go/service/autoscaling/autoscalingiface"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/ec2/ec2iface"
	"golang.org/x/xerrors"

	"github.com/abicky/ecsmec/internal/const/autoscalingconst"
	"github.com/abicky/ecsmec/internal/sliceutil"
)

type AutoScalingGroup struct {
	OriginalDesiredCapacity *int64
	OriginalMaxSize         *int64
	StateSavedAt            *time.Time

	*autoscaling.Group

	asSvc  autoscalingiface.AutoScalingAPI
	ec2Svc ec2iface.EC2API
	name   string
}

func NewAutoScalingGroup(name string, asSvc autoscalingiface.AutoScalingAPI, ec2Svc ec2iface.EC2API) (*AutoScalingGroup, error) {
	asg := AutoScalingGroup{asSvc: asSvc, ec2Svc: ec2Svc, name: name}
	err := asg.reload()
	if err != nil {
		return nil, err
	}
	return &asg, nil
}

func (asg *AutoScalingGroup) ReplaceInstances(drainer Drainer) error {
	oldInstanceIDs := make([]string, 0)
	baseTime := asg.StateSavedAt
	if baseTime == nil {
		baseTime = aws.Time(time.Now())
	}

	err := asg.fetchInstances(func(i *ec2.Instance) error {
		if i.LaunchTime.Before(*baseTime) {
			oldInstanceIDs = append(oldInstanceIDs, *i.InstanceId)
		}
		return nil
	})
	if err != nil {
		return xerrors.Errorf("failed to fetch old instance IDs: %w", err)
	}

	if err := asg.launchNewInstances(len(oldInstanceIDs)); err != nil {
		return xerrors.Errorf("failed to launch new instances: %w", err)
	}

	if err := asg.terminateInstances(*asg.DesiredCapacity-*asg.OriginalDesiredCapacity, drainer); err != nil {
		return xerrors.Errorf("failed to terminate instances: %w", err)
	}

	if err := asg.restoreState(); err != nil {
		return xerrors.Errorf("failed to restore the auto scaling group: %w", err)
	}

	return nil
}

func (asg *AutoScalingGroup) reload() error {
	resp, err := asg.asSvc.DescribeAutoScalingGroups(&autoscaling.DescribeAutoScalingGroupsInput{
		AutoScalingGroupNames: []*string{aws.String(asg.name)},
	})
	if err != nil {
		return xerrors.Errorf("failed to describe the auto scaling group: %w", err)
	}

	if resp.AutoScalingGroups == nil {
		return xerrors.Errorf("the auto scaling group \"%s\" doesn't exist", asg.name)
	}

	group := resp.AutoScalingGroups[0]
	originalDesiredCapacity := *group.DesiredCapacity
	originalMaxSize := *group.MaxSize
	var stateSavedAt time.Time
	for _, t := range group.Tags {
		switch *t.Key {
		case "ecsmec:OriginalDesiredCapacity":
			originalDesiredCapacity, err = strconv.ParseInt(*t.Value, 10, 64)
			if err != nil {
				return xerrors.Errorf("ecsmec:OriginalDesiredCapacity is invalid (%s): %w", *t.Value, err)
			}
		case "ecsmec:OriginalMaxSize":
			originalMaxSize, err = strconv.ParseInt(*t.Value, 10, 64)
			if err != nil {
				return xerrors.Errorf("ecsmec:OriginalMaxSize is invalid (%s): %w", *t.Value, err)
			}
		case "ecsmec:StateSavedAt":
			stateSavedAt, err = time.Parse(time.RFC3339, *t.Value)
			if err != nil {
				return xerrors.Errorf("ecsmec:StateSavedAt is invalid (%s): %w", *t.Value, err)
			}
			asg.StateSavedAt = &stateSavedAt
		}
	}

	asg.Group = group
	asg.OriginalDesiredCapacity = &originalDesiredCapacity
	asg.OriginalMaxSize = &originalMaxSize

	return nil
}

func (asg *AutoScalingGroup) fetchInstances(callback func(*ec2.Instance) error) error {
	ids := make([]*string, len(asg.Instances))
	for i, instance := range asg.Instances {
		ids[i] = instance.InstanceId
	}

	resp, err := asg.ec2Svc.DescribeInstances(&ec2.DescribeInstancesInput{
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

func (asg *AutoScalingGroup) launchNewInstances(oldInstanceCount int) error {
	if oldInstanceCount == 0 {
		return nil
	}
	requiredCount := int64(oldInstanceCount)

	// The new desired capacity must be a multiple of the number of availability zones,
	// otherwise AZRebalance will terminate some instances unexpectedly.
	// Assume that there are following instances on each availability zone:
	//   ap-northeast-1a: 2, ap-northeast-1c: 1, ap-northeast-1d: 2
	// After increasing the desired capacity to 10, that is, launching new instances as many as the old instances,
	// the number of instances will change as below:
	//   ap-northeast-1a: 3 (old: 2, new: 1), ap-northeast-1c: 4 (old: 1, new: 3), ap-northeast-1d: 3 (old: 2, new: 1)
	// After terminating old instances, the number of instances will change as below:
	//   ap-northeast-1a: 1, ap-northeast-1c: 3, ap-northeast-1d: 1
	// AZRebalance will launch another instance in ap-northeast-1a or ap-northeast-1d and terminate one
	// in ap-northeast-1c without draining it.
	if *asg.OriginalDesiredCapacity%int64(len(asg.AvailabilityZones)) > 0 {
		requiredCount += int64(len(asg.AvailabilityZones)) - *asg.OriginalDesiredCapacity%int64(len(asg.AvailabilityZones))
	}

	if err := asg.waitUntilInstancesInService(*asg.DesiredCapacity); err != nil {
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

	if err := asg.saveCurrentState(); err != nil {
		return xerrors.Errorf("failed to save the current state: %w", err)
	}

	log.Printf("Update the auto scaling group \"%s\": DesirdCapacity: %d, MaxSize: %d\n",
		*asg.AutoScalingGroupName, newDesiredCapacity, newDesiredMaxSize)
	_, err := asg.asSvc.UpdateAutoScalingGroup(&autoscaling.UpdateAutoScalingGroupInput{
		AutoScalingGroupName: asg.AutoScalingGroupName,
		DesiredCapacity:      aws.Int64(newDesiredCapacity),
		MaxSize:              aws.Int64(newDesiredMaxSize),
	})
	if err != nil {
		return xerrors.Errorf("failed to update the auto scaling group: %w", err)
	}

	if err := asg.waitUntilInstancesInService(newDesiredCapacity); err != nil {
		return xerrors.Errorf("failed to wait until %d instances are in service: %w", newDesiredCapacity, err)
	}

	return asg.reload()
}

func (asg *AutoScalingGroup) terminateInstances(count int64, drainer Drainer) error {
	if count == 0 {
		return nil
	}

	// Sort instanceIDs to prevent AZRebalance from terminating instances unexpectedly
	sortedInstanceIDs, err := asg.fetchSortedInstanceIDs(count)
	if err != nil {
		return xerrors.Errorf("failed to fetch sorted instance IDs: %w", err)
	}

	if err := drainer.Drain(sortedInstanceIDs); err != nil {
		return xerrors.Errorf("failed to drain instances: %w", err)
	}

	for ids := range sliceutil.ChunkSlice(aws.StringSlice(sortedInstanceIDs), autoscalingconst.MaxDetachableInstances) {
		log.Println("Detach instances:", aws.StringValueSlice(ids))
		_, err := asg.asSvc.DetachInstances(&autoscaling.DetachInstancesInput{
			AutoScalingGroupName:           asg.AutoScalingGroupName,
			InstanceIds:                    ids,
			ShouldDecrementDesiredCapacity: aws.Bool(true),
		})
		if err != nil {
			return xerrors.Errorf("failed to detach instances: %w", err)
		}
	}

	log.Println("Terminate instances:", sortedInstanceIDs)
	_, err = asg.ec2Svc.TerminateInstances(&ec2.TerminateInstancesInput{
		InstanceIds: aws.StringSlice(sortedInstanceIDs),
	})
	if err != nil {
		return xerrors.Errorf("failed to terminate the instances: %w", err)
	}

	err = asg.ec2Svc.WaitUntilInstanceTerminated(&ec2.DescribeInstancesInput{
		InstanceIds: aws.StringSlice(sortedInstanceIDs),
	})
	if err != nil {
		return xerrors.Errorf("failed to terminate the instances: %w", err)
	}

	return asg.reload()
}

func (asg *AutoScalingGroup) restoreState() error {
	if *asg.DesiredCapacity != *asg.OriginalDesiredCapacity {
		return xerrors.Errorf("can't restore the state unless the desired capacity is %d", *asg.OriginalDesiredCapacity)
	}

	log.Printf("Update the auto scaling group \"%s\": MaxSize: %d\n", *asg.AutoScalingGroupName, *asg.OriginalMaxSize)

	_, err := asg.asSvc.UpdateAutoScalingGroup(&autoscaling.UpdateAutoScalingGroupInput{
		AutoScalingGroupName: asg.AutoScalingGroupName,
		MaxSize:              asg.OriginalMaxSize,
	})
	if err != nil {
		return xerrors.Errorf("failed to update the auto scaling group: %w", err)
	}

	_, err = asg.asSvc.DeleteTags(&autoscaling.DeleteTagsInput{
		Tags: []*autoscaling.Tag{
			asg.createTag("ecsmec:OriginalDesiredCapacity", fmt.Sprint(*asg.OriginalDesiredCapacity)),
			asg.createTag("ecsmec:OriginalMaxSize", fmt.Sprint(*asg.OriginalMaxSize)),
			asg.createTag("ecsmec:StateSavedAt", fmt.Sprint(asg.StateSavedAt.Format(time.RFC3339))),
		},
	})
	if err != nil {
		return xerrors.Errorf("failed to delete tags: %w", err)
	}

	return asg.reload()
}

func (asg *AutoScalingGroup) saveCurrentState() error {
	var stateSavedAt time.Time
	if asg.StateSavedAt != nil {
		stateSavedAt = *asg.StateSavedAt
	} else {
		stateSavedAt = time.Now().UTC()
	}

	_, err := asg.asSvc.CreateOrUpdateTags(&autoscaling.CreateOrUpdateTagsInput{
		Tags: []*autoscaling.Tag{
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

func (asg *AutoScalingGroup) createTag(key string, value string) *autoscaling.Tag {
	return &autoscaling.Tag{
		Key:               aws.String(key),
		PropagateAtLaunch: aws.Bool(false),
		ResourceId:        asg.AutoScalingGroupName,
		ResourceType:      aws.String("auto-scaling-group"),
		Value:             aws.String(value),
	}
}

func (asg *AutoScalingGroup) waitUntilInstancesInService(capacity int64) error {
	// WaitUntilGroupInService doesn't work even if the MinSize is equal to the DesiredCapacity
	// (https://github.com/aws/aws-sdk-go/issues/2478),
	// so wait manually
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	timeout := 5 * time.Minute
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		resp, err := asg.asSvc.DescribeAutoScalingGroups(&autoscaling.DescribeAutoScalingGroupsInput{
			AutoScalingGroupNames: []*string{asg.AutoScalingGroupName},
		})
		if err != nil {
			return xerrors.Errorf("failed to describe the auto scaling group: %w", err)
		}

		healthyInstanceCnt := int64(0)
		for _, i := range resp.AutoScalingGroups[0].Instances {
			if *i.LifecycleState == "InService" {
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
		}
	}
}

func (asg *AutoScalingGroup) fetchSortedInstanceIDs(count int64) ([]string, error) {
	instances := make([]*ec2.Instance, 0, *asg.DesiredCapacity)
	err := asg.fetchInstances(func(i *ec2.Instance) error {
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
	azToInstances := make(map[string][]*ec2.Instance)
	for _, i := range instances {
		az := *i.Placement.AvailabilityZone
		if !sliceutil.Contains(azs, az) {
			azs = append(azs, az)
		}
		azToInstances[az] = append(azToInstances[az], i)
	}

	sort.SliceStable(azs, func(i, j int) bool {
		return len(azToInstances[azs[i]]) > len(azToInstances[azs[j]])
	})

	sortedInstanceIDs := make([]string, 0, count)
Loop:
	for {
		for _, az := range azs {
			if int64(len(sortedInstanceIDs)) == count {
				break Loop
			}
			var i *ec2.Instance
			i, azToInstances[az] = azToInstances[az][0], azToInstances[az][1:]
			sortedInstanceIDs = append(sortedInstanceIDs, *i.InstanceId)
		}
	}

	return sortedInstanceIDs, nil
}
