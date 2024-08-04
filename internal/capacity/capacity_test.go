package capacity_test

import (
	"fmt"
	"io"
	"log"
	"math/rand"
	"os"
	"testing"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/autoscaling"
)

func TestMain(m *testing.M) {
	log.SetOutput(io.Discard)
	os.Exit(m.Run())
}

func createInstance(az string) *autoscaling.Instance {
	return createInstances(az, 1)[0]
}

func createInstances(az string, size int) []*autoscaling.Instance {
	azChar := az[len(az)-1:]
	instances := make([]*autoscaling.Instance, size)
	for i := 0; i < size; i++ {
		instances[i] = &autoscaling.Instance{
			AvailabilityZone: aws.String(az),
			InstanceId:       aws.String(fmt.Sprintf("i-%s%08x%08d", azChar, rand.Int31(), i)),
			LifecycleState:   aws.String("InService"),
		}
	}

	return instances
}
