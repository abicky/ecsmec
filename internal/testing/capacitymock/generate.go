package capacitymock

//go:generate mockgen -package capacitymock -destination mocks.go github.com/abicky/ecsmec/internal/capacity AutoScalingAPI,Drainer,EC2API,ECSAPI,Poller,SQSAPI
