#!/usr/bin/env bash
# Create every topic and its dead-letter topic (docs/08 §1). Idempotent; auto-create is disabled on the broker.
set -euo pipefail

BOOTSTRAP="${KAFKA_BOOTSTRAP:-ecom-kafka:9092}"
KT=/opt/kafka/bin/kafka-topics.sh

TOPICS=(
  # domain events
  auth.shop.registered
  auth.user.registered
  auth.email-verification.requested
  catalog.product.created
  catalog.product.updated
  discount.discount.created
  order.order.confirmed
  order.order.cancelled
  # saga commands and replies
  inventory.commands
  discount.commands
  payment.commands
  order.saga.replies
)

for t in "${TOPICS[@]}"; do
  "$KT" --bootstrap-server "$BOOTSTRAP" --create --if-not-exists --topic "$t"       --partitions 3 --replication-factor 1
  "$KT" --bootstrap-server "$BOOTSTRAP" --create --if-not-exists --topic "$t.dlq"   --partitions 1 --replication-factor 1
done
echo "topics ready:"; "$KT" --bootstrap-server "$BOOTSTRAP" --list
