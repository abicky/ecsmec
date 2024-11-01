package capacity_test

import (
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"os"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	autoscalingtypes "github.com/aws/aws-sdk-go-v2/service/autoscaling/types"
)

func TestMain(m *testing.M) {
	log.SetOutput(io.Discard)
	os.Exit(m.Run())
}

func createInstance(az string) autoscalingtypes.Instance {
	return createInstances(az, 1)[0]
}

func createInstances(az string, size int) []autoscalingtypes.Instance {
	azChar := az[len(az)-1:]
	instances := make([]autoscalingtypes.Instance, size)
	for i := range size {
		instances[i] = autoscalingtypes.Instance{
			AvailabilityZone: aws.String(az),
			InstanceId:       aws.String(fmt.Sprintf("i-%s%08x%08d", azChar, rand.Uint32(), i)),
			LifecycleState:   "InService",
		}
	}

	return instances
}
