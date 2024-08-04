package capacity

import (
	"context"
	"log"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"golang.org/x/xerrors"

	"github.com/abicky/ecsmec/internal/const/sqsconst"
)

type Poller interface {
	Poll(context.Context, func([]sqstypes.Message) ([]sqstypes.DeleteMessageBatchRequestEntry, error))
	PollOnce(context.Context, func([]sqstypes.Message) ([]sqstypes.DeleteMessageBatchRequestEntry, error), int32) error
}

type SQSQueuePoller struct {
	queueURL string
	sqsSvc   SQSAPI
}

func NewSQSQueuePoller(queueURL string, sqsSvc SQSAPI) *SQSQueuePoller {
	return &SQSQueuePoller{
		queueURL: queueURL,
		sqsSvc:   sqsSvc,
	}
}

func (p *SQSQueuePoller) Poll(ctx context.Context, callback func([]sqstypes.Message) ([]sqstypes.DeleteMessageBatchRequestEntry, error)) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
			if err := p.PollOnce(ctx, callback, sqsconst.WaitTimeSecondsForLongPolling); err != nil {
				log.Printf("[WARNING] %+v\n", err)
			}
		}
	}
}

func (p *SQSQueuePoller) PollOnce(ctx context.Context, callback func([]sqstypes.Message) ([]sqstypes.DeleteMessageBatchRequestEntry, error), waitTimeSeconds int32) error {
	resp, err := p.sqsSvc.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		MaxNumberOfMessages: sqsconst.MaxReceivableMessages,
		QueueUrl:            aws.String(p.queueURL),
		VisibilityTimeout:   10,
		WaitTimeSeconds:     waitTimeSeconds,
	})
	if err != nil {
		return xerrors.Errorf("failed to receive messages: %w", err)
	}

	entries, err := callback(resp.Messages)
	if err != nil {
		return err
	}

	if len(entries) == 0 {
		return nil
	}

	_, err = p.sqsSvc.DeleteMessageBatch(ctx, &sqs.DeleteMessageBatchInput{
		Entries:  entries,
		QueueUrl: aws.String(p.queueURL),
	})
	if err != nil {
		return xerrors.Errorf("failed to delete messages: %w", err)
	}

	return nil
}
