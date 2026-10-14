#!/bin/bash
# Runs inside LocalStack once it is ready (/etc/localstack/init/ready.d).
set -euo pipefail

awslocal sqs create-queue \
  --queue-name wager-events.fifo \
  --attributes FifoQueue=true,ContentBasedDeduplication=false

echo "queues ready"
