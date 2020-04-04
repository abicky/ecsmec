package capacity

import (
	"log"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/ec2/ec2iface"
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
