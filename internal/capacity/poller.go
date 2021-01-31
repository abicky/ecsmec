package capacity

import (
	"context"
	"log"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/sqs"
	"github.com/aws/aws-sdk-go/service/sqs/sqsiface"
	"golang.org/x/xerrors"

	"github.com/abicky/ecsmec/internal/const/sqsconst"
)

type Poller interface {
	Poll(context.Context, func([]*sqs.Message) ([]*sqs.DeleteMessageBatchRequestEntry, error))
	PollOnce(func([]*sqs.Message) ([]*sqs.DeleteMessageBatchRequestEntry, error), int64) error
}

type SQSQueuePoller struct {
	queueURL string
	sqsSvc   sqsiface.SQSAPI
}

func NewSQSQueuePoller(queueURL string, sqsSvc sqsiface.SQSAPI) *SQSQueuePoller {
	return &SQSQueuePoller{
		queueURL: queueURL,
		sqsSvc:   sqsSvc,
	}
}

func (p *SQSQueuePoller) Poll(ctx context.Context, callback func([]*sqs.Message) ([]*sqs.DeleteMessageBatchRequestEntry, error)) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
			if err := p.PollOnce(callback, sqsconst.WaitTimeSecondsForLongPolling); err != nil {
				log.Printf("[WARNING] %+v\n", err)
			}
		}
	}
}

func (p *SQSQueuePoller) PollOnce(callback func([]*sqs.Message) ([]*sqs.DeleteMessageBatchRequestEntry, error), waitTimeSeconds int64) error {
	resp, err := p.sqsSvc.ReceiveMessage(&sqs.ReceiveMessageInput{
		MaxNumberOfMessages: aws.Int64(sqsconst.MaxReceivableMessages),
		QueueUrl:            aws.String(p.queueURL),
		VisibilityTimeout:   aws.Int64(10),
		WaitTimeSeconds:     aws.Int64(waitTimeSeconds),
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

	_, err = p.sqsSvc.DeleteMessageBatch(&sqs.DeleteMessageBatchInput{
		Entries:  entries,
		QueueUrl: aws.String(p.queueURL),
	})
	if err != nil {
		return xerrors.Errorf("failed to delete messages: %w", err)
	}

	return nil
}
