package capacity

import (
	"context"
	"log"
	"strings"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/ec2/ec2iface"
	"github.com/aws/aws-sdk-go/service/sqs"
	"golang.org/x/xerrors"
)

type SpotFleetRequest struct {
	SpotFleetRequestConfigData *ec2.SpotFleetRequestConfigData

	*ec2.SpotFleetRequestConfig

	ec2Svc ec2iface.EC2API
	id     string
}

func NewSpotFleetRequest(id string, ec2Svc ec2iface.EC2API) (*SpotFleetRequest, error) {
	sfr := SpotFleetRequest{ec2Svc: ec2Svc, id: id}
	if err := sfr.reload(); err != nil {
		return nil, err
	}
	return &sfr, nil
}

func (sfr *SpotFleetRequest) TerminateAllInstances(drainer Drainer) error {
	instanceIDs, err := sfr.fetchInstanceIDs()
	if err != nil {
		return xerrors.Errorf("failed to fetch instance IDs: %w", err)
	}

	if len(instanceIDs) == 0 {
		return nil
	}

	if *sfr.SpotFleetRequestConfigData.Type == "maintain" && !strings.HasPrefix(*sfr.SpotFleetRequestState, "cancelled") {
		return xerrors.Errorf("the spot fleet request with the type \"maintain\" must be canceled, but the state is \"%s\"", *sfr.SpotFleetRequestState)
	}

	if err := drainer.Drain(instanceIDs); err != nil {
		return xerrors.Errorf("failed to drain instances: %w", err)
	}

	log.Println("Terminate instances:", instanceIDs)
	_, err = sfr.ec2Svc.TerminateInstances(&ec2.TerminateInstancesInput{
		InstanceIds: aws.StringSlice(instanceIDs),
	})
	if err != nil {
		return xerrors.Errorf("failed to terminate the instances: %w", err)
	}

	err = sfr.ec2Svc.WaitUntilInstanceTerminated(&ec2.DescribeInstancesInput{
		InstanceIds: aws.StringSlice(instanceIDs),
	})
	if err != nil {
		return xerrors.Errorf("failed to terminate the instances: %w", err)
	}

	return nil
}

func (sfr *SpotFleetRequest) ReduceCapacity(amount int64, drainer Drainer, poller Poller) error {
	if *sfr.SpotFleetRequestConfigData.TargetCapacity-amount < 0 {
		amount = *sfr.SpotFleetRequestConfigData.TargetCapacity
	}
	if amount == 0 {
		return nil
	}

	capacityPerInstance, err := sfr.weightedCapacity()
	if err != nil {
		return xerrors.Errorf("failed to calculate capacity per instance: %w", err)
	}

	drainedCount := int64(0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		poller.Poll(ctx, func(messages []*sqs.Message) ([]*sqs.DeleteMessageBatchRequestEntry, error) {
			entries, err := drainer.ProcessInterruptions(messages)
			if err != nil {
				return nil, xerrors.Errorf("failed to process interruptions: %w", err)
			}
			atomic.AddInt64(&drainedCount, int64(len(entries))*capacityPerInstance)
			return entries, nil
		})
	}()

	newTargetCapacity := *sfr.SpotFleetRequestConfigData.TargetCapacity - amount
	log.Printf("Modify the spot fleet request \"%s\": TargetCapacity: %d\n", sfr.id, newTargetCapacity)
	_, err = sfr.ec2Svc.ModifySpotFleetRequest(&ec2.ModifySpotFleetRequestInput{
		SpotFleetRequestId: aws.String(sfr.id),
		TargetCapacity:     aws.Int64(newTargetCapacity),
	})
	if err != nil {
		return xerrors.Errorf("failed to modify the spot fleet request: %w", err)
	}

	timeout := 5 * time.Minute
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	log.Printf("Wait for instances to be drained")
	for {
		if atomic.LoadInt64(&drainedCount) > amount-capacityPerInstance {
			break
		}
		time.Sleep(100 * time.Millisecond)

		select {
		case <-timer.C:
			return xerrors.Errorf("all the spot fleet instances weren't drained within %v", timeout)
		default:
		}
	}

	return nil
}

func (sfr *SpotFleetRequest) fetchInstanceIDs() ([]string, error) {
	resp, err := sfr.ec2Svc.DescribeSpotFleetInstances(&ec2.DescribeSpotFleetInstancesInput{
		SpotFleetRequestId: aws.String(sfr.id),
	})
	if err != nil {
		return nil, xerrors.Errorf("failed to describe spot fleet instances: %w", err)
	}

	ids := make([]string, len(resp.ActiveInstances))
	for i, instance := range resp.ActiveInstances {
		ids[i] = *instance.InstanceId
	}

	return ids, nil
}

func (sfr *SpotFleetRequest) reload() error {
	resp, err := sfr.ec2Svc.DescribeSpotFleetRequests(&ec2.DescribeSpotFleetRequestsInput{
		SpotFleetRequestIds: []*string{aws.String(sfr.id)},
	})
	if err != nil {
		return xerrors.Errorf("failed to describe the spot fleet request: %w", err)
	}

	sfr.SpotFleetRequestConfig = resp.SpotFleetRequestConfigs[0]
	sfr.SpotFleetRequestConfigData = resp.SpotFleetRequestConfigs[0].SpotFleetRequestConfig

	return nil
}

func (sfr *SpotFleetRequest) weightedCapacity() (int64, error) {
	weightedCapacity := int64(0)

	// Spot Fleet Requests with a LaunchTemplate
	for _, conf := range sfr.SpotFleetRequestConfigData.LaunchTemplateConfigs {
		for _, o := range conf.Overrides {
			if *o.WeightedCapacity != float64(int64(*o.WeightedCapacity)) {
				return 0, xerrors.Errorf("currently float weighted capacities are not supported")
			}
			if weightedCapacity == 0 {
				weightedCapacity = int64(*o.WeightedCapacity)
			}
			if weightedCapacity != int64(*o.WeightedCapacity) {
				return 0, xerrors.Errorf("currently mixed weighted capacities are not supported")
			}
		}
	}

	// Spot Fleet Requests without a LaunchTemplate
	for _, spec := range sfr.SpotFleetRequestConfigData.LaunchSpecifications {
		if spec.WeightedCapacity == nil {
			continue
		}
		if *spec.WeightedCapacity != float64(int64(*spec.WeightedCapacity)) {
			return 0, xerrors.Errorf("currently float weighted capacities are not supported")
		}
		if weightedCapacity == 0 {
			weightedCapacity = int64(*spec.WeightedCapacity)
		}
		if weightedCapacity != int64(*spec.WeightedCapacity) {
			return 0, xerrors.Errorf("currently mixed weighted capacities are not supported")
		}
	}

	if weightedCapacity == 0 {
		weightedCapacity = 1
	}

	return weightedCapacity, nil
}
