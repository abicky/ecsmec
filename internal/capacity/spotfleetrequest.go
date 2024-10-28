package capacity

import (
	"context"
	"log"
	"strings"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"golang.org/x/xerrors"
)

type SpotFleetRequest struct {
	SpotFleetRequestConfigData *ec2types.SpotFleetRequestConfigData

	ec2types.SpotFleetRequestConfig

	ec2Svc EC2API
	id     string
}

func NewSpotFleetRequest(id string, ec2Svc EC2API) (*SpotFleetRequest, error) {
	sfr := SpotFleetRequest{ec2Svc: ec2Svc, id: id}
	if err := sfr.reload(context.Background()); err != nil {
		return nil, err
	}
	return &sfr, nil
}

func (sfr *SpotFleetRequest) TerminateAllInstances(ctx context.Context, drainer Drainer) error {
	instanceIDs, err := sfr.fetchInstanceIDs(ctx)
	if err != nil {
		return xerrors.Errorf("failed to fetch instance IDs: %w", err)
	}

	if len(instanceIDs) == 0 {
		return nil
	}

	if sfr.SpotFleetRequestConfigData.Type == ec2types.FleetTypeMaintain && !strings.HasPrefix(string(sfr.SpotFleetRequestState), "cancelled") {
		return xerrors.Errorf("the spot fleet request with the type \"%s\" must be canceled, but the state is \"%s\"", ec2types.FleetTypeMaintain, sfr.SpotFleetRequestState)
	}

	if err := drainer.Drain(ctx, instanceIDs); err != nil {
		return xerrors.Errorf("failed to drain instances: %w", err)
	}

	log.Println("Terminate instances:", instanceIDs)
	_, err = sfr.ec2Svc.TerminateInstances(ctx, &ec2.TerminateInstancesInput{
		InstanceIds: instanceIDs,
	})
	if err != nil {
		return xerrors.Errorf("failed to terminate the instances: %w", err)
	}

	waiter := ec2.NewInstanceTerminatedWaiter(sfr.ec2Svc, func(o *ec2.InstanceTerminatedWaiterOptions) {
		o.MaxDelay = 15 * time.Second
	})
	err = waiter.Wait(ctx, &ec2.DescribeInstancesInput{
		InstanceIds: instanceIDs,
	}, 10*time.Minute)
	if err != nil {
		return xerrors.Errorf("failed to terminate the instances: %w", err)
	}

	return nil
}

func (sfr *SpotFleetRequest) ReduceCapacity(ctx context.Context, amount int32, drainer Drainer, poller Poller) error {
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

	drainedCount := &atomic.Int32{}
	ctxForPoll, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		poller.Poll(ctxForPoll, func(messages []sqstypes.Message) ([]sqstypes.DeleteMessageBatchRequestEntry, error) {
			entries, err := drainer.ProcessInterruptions(ctx, messages)
			if err != nil {
				return nil, xerrors.Errorf("failed to process interruptions: %w", err)
			}
			drainedCount.Add(int32(len(entries)) * capacityPerInstance)
			return entries, nil
		})
	}()

	newTargetCapacity := *sfr.SpotFleetRequestConfigData.TargetCapacity - amount
	log.Printf("Modify the spot fleet request \"%s\": TargetCapacity: %d\n", sfr.id, newTargetCapacity)
	_, err = sfr.ec2Svc.ModifySpotFleetRequest(ctx, &ec2.ModifySpotFleetRequestInput{
		SpotFleetRequestId: aws.String(sfr.id),
		TargetCapacity:     aws.Int32(newTargetCapacity),
	})
	if err != nil {
		return xerrors.Errorf("failed to modify the spot fleet request: %w", err)
	}

	timeout := 5 * time.Minute
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	log.Printf("Wait for instances to be drained")
	for {
		if drainedCount.Load() > amount-capacityPerInstance {
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

func (sfr *SpotFleetRequest) fetchInstanceIDs(ctx context.Context) ([]string, error) {
	resp, err := sfr.ec2Svc.DescribeSpotFleetInstances(ctx, &ec2.DescribeSpotFleetInstancesInput{
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

func (sfr *SpotFleetRequest) reload(ctx context.Context) error {
	resp, err := sfr.ec2Svc.DescribeSpotFleetRequests(ctx, &ec2.DescribeSpotFleetRequestsInput{
		SpotFleetRequestIds: []string{sfr.id},
	})
	if err != nil {
		return xerrors.Errorf("failed to describe the spot fleet request: %w", err)
	}

	sfr.SpotFleetRequestConfig = resp.SpotFleetRequestConfigs[0]
	sfr.SpotFleetRequestConfigData = resp.SpotFleetRequestConfigs[0].SpotFleetRequestConfig

	return nil
}

func (sfr *SpotFleetRequest) weightedCapacity() (int32, error) {
	weightedCapacity := int32(0)

	// Spot Fleet Requests with a LaunchTemplate
	for _, conf := range sfr.SpotFleetRequestConfigData.LaunchTemplateConfigs {
		for _, o := range conf.Overrides {
			if *o.WeightedCapacity != float64(int32(*o.WeightedCapacity)) {
				return 0, xerrors.Errorf("currently float weighted capacities are not supported")
			}
			if weightedCapacity == 0 {
				weightedCapacity = int32(*o.WeightedCapacity)
			}
			if weightedCapacity != int32(*o.WeightedCapacity) {
				return 0, xerrors.Errorf("currently mixed weighted capacities are not supported")
			}
		}
	}

	// Spot Fleet Requests without a LaunchTemplate
	for _, spec := range sfr.SpotFleetRequestConfigData.LaunchSpecifications {
		if spec.WeightedCapacity == nil {
			continue
		}
		if *spec.WeightedCapacity != float64(int32(*spec.WeightedCapacity)) {
			return 0, xerrors.Errorf("currently float weighted capacities are not supported")
		}
		if weightedCapacity == 0 {
			weightedCapacity = int32(*spec.WeightedCapacity)
		}
		if weightedCapacity != int32(*spec.WeightedCapacity) {
			return 0, xerrors.Errorf("currently mixed weighted capacities are not supported")
		}
	}

	if weightedCapacity == 0 {
		weightedCapacity = 1
	}

	return weightedCapacity, nil
}
