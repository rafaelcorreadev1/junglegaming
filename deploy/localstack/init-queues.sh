#!/bin/bash
# Runs inside LocalStack once it is ready (/etc/localstack/init/ready.d).
set -euo pipefail

REGION="${AWS_DEFAULT_REGION:-us-east-1}"
ACCOUNT=000000000000
ARN="arn:aws:sqs:${REGION}:${ACCOUNT}"
ROLE="arn:aws:iam::${ACCOUNT}:role"
MAX_RECEIVE_COUNT=5
TMP=$(mktemp -d)

# Attribute values that are themselves JSON go through a file to avoid shell quoting.
create_queue() {
  awslocal sqs create-queue --queue-name "$1" --attributes "file://$2" >/dev/null
  echo "created $1"
}

cat >"$TMP/dlq.json" <<EOF
{
  "FifoQueue": "true",
  "ContentBasedDeduplication": "false",
  "MessageRetentionPeriod": "1209600",
  "RedriveAllowPolicy": "{\"redrivePermission\":\"byQueue\",\"sourceQueueArns\":[\"${ARN}:wager-transactions.fifo\"]}",
  "Policy": "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Sid\":\"ServiceDeadLetters\",\"Effect\":\"Allow\",\"Principal\":{\"AWS\":\"${ROLE}/wagering-service\"},\"Action\":\"sqs:SendMessage\",\"Resource\":\"${ARN}:wager-transactions-dlq.fifo\"},{\"Sid\":\"OperatorsInspect\",\"Effect\":\"Allow\",\"Principal\":{\"AWS\":\"${ROLE}/wagering-operator\"},\"Action\":[\"sqs:ReceiveMessage\",\"sqs:DeleteMessage\",\"sqs:StartMessageMoveTask\",\"sqs:GetQueueAttributes\"],\"Resource\":\"${ARN}:wager-transactions-dlq.fifo\"}]}"
}
EOF
create_queue wager-transactions-dlq.fifo "$TMP/dlq.json"

cat >"$TMP/wagers.json" <<EOF
{
  "FifoQueue": "true",
  "ContentBasedDeduplication": "false",
  "VisibilityTimeout": "30",
  "ReceiveMessageWaitTimeSeconds": "20",
  "RedrivePolicy": "{\"deadLetterTargetArn\":\"${ARN}:wager-transactions-dlq.fifo\",\"maxReceiveCount\":\"${MAX_RECEIVE_COUNT}\"}",
  "Policy": "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Sid\":\"ProvidersSend\",\"Effect\":\"Allow\",\"Principal\":{\"AWS\":[\"${ROLE}/provider-a\",\"${ROLE}/provider-b\"]},\"Action\":\"sqs:SendMessage\",\"Resource\":\"${ARN}:wager-transactions.fifo\"},{\"Sid\":\"ServiceConsumes\",\"Effect\":\"Allow\",\"Principal\":{\"AWS\":\"${ROLE}/wagering-service\"},\"Action\":[\"sqs:ReceiveMessage\",\"sqs:DeleteMessage\",\"sqs:ChangeMessageVisibility\",\"sqs:GetQueueAttributes\"],\"Resource\":\"${ARN}:wager-transactions.fifo\"}]}"
}
EOF
create_queue wager-transactions.fifo "$TMP/wagers.json"

cat >"$TMP/events.json" <<EOF
{
  "FifoQueue": "true",
  "ContentBasedDeduplication": "false",
  "Policy": "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Sid\":\"ServicePublishes\",\"Effect\":\"Allow\",\"Principal\":{\"AWS\":\"${ROLE}/wagering-service\"},\"Action\":\"sqs:SendMessage\",\"Resource\":\"${ARN}:wager-events.fifo\"},{\"Sid\":\"WalletServiceConsumes\",\"Effect\":\"Allow\",\"Principal\":{\"AWS\":\"${ROLE}/wallet-service\"},\"Action\":[\"sqs:ReceiveMessage\",\"sqs:DeleteMessage\",\"sqs:ChangeMessageVisibility\",\"sqs:GetQueueAttributes\"],\"Resource\":\"${ARN}:wager-events.fifo\"}]}"
}
EOF
# Created last: the Compose healthcheck waits for this queue, so every queue exists once it passes.
create_queue wager-events.fifo "$TMP/events.json"

rm -rf "$TMP"
echo "queues ready"
