package sqsconst

const (
	// ReceiveMessage can receive messages up to this value
	// cf. https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_ReceiveMessage.html
	MaxReceivableMessages = 10
	// ReceiveMessage can wait messages for up to this value
	// cf. https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-short-and-long-polling.html#sqs-long-polling
	WaitTimeSecondsForLongPolling = 20
)
