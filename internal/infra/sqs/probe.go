package sqs

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"wagering-service/internal/config"
)

// Probe checks every queue the service reads or writes, not only that SQS answers.
type Probe struct {
	client    *sqs.Client
	queueURLs []string
}

func NewProbe(client *sqs.Client, cfg config.Config) *Probe {
	return &Probe{client: client, queueURLs: []string{cfg.WagerQueueURL, cfg.WagerDLQURL, cfg.OutboxQueueURL}}
}

func (p *Probe) Name() string { return "sqs" }

func (p *Probe) Ping(ctx context.Context) error {
	for _, url := range p.queueURLs {
		_, err := p.client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
			QueueUrl:       aws.String(url),
			AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn},
		})
		if err != nil {
			return fmt.Errorf("queue %s: %w", url, err)
		}
	}
	return nil
}
