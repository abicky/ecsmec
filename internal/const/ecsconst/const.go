package ecsconst

const (
	// DescribeServices can describe services up to this value
	// cf. https://docs.aws.amazon.com/AmazonECS/latest/APIReference/API_DescribeServices.html
	MaxDescribableServices = 10
	// DescribeTasks can describe tasks up to this value
	// cf. https://docs.aws.amazon.com/AmazonECS/latest/APIReference/API_DescribeTasks.html
	MaxDescribableTasks = 100
	// ListContainerInstances can list container instances up to this value
	// cf. https://docs.aws.amazon.com/AmazonECS/latest/APIReference/API_ListContainerInstances.html
	MaxListableContainerInstances = 100
	// UpdateContainerInstancesState can update instances' state up to this value, and if you try to update more than
	// 10 instances, the error "InvalidParameterException: instanceIds can have at most 10 items." occurs.
	MaxUpdatableContainerInstancesState = 10
)
