#!/usr/bin/env python3
"""
Console consumer for the CRSProxyAgent "discovery-data" Kafka topic.

Reads node discovery events (NODE_ADDED / NODE_UPDATED / NODE_REMOVED)
published by the operator's NodeReconciler and pretty-prints them.

Usage:
    python discovery_consumer.py
    python discovery_consumer.py --brokers localhost:9092
    python discovery_consumer.py --topic discovery-data --latest-only
"""

import argparse
import json
import sys
from datetime import datetime, timezone

from kafka import KafkaConsumer
from kafka.errors import KafkaError

DEFAULT_TOPIC = "discovery-data"


def pretty_print(topic: str, key, value, partition: int, offset: int):
    ts = datetime.now(timezone.utc).strftime("%Y-%m-%d %H:%M:%S UTC")
    print("=" * 100)
    print(f"[{ts}]  topic={topic}  partition={partition}  offset={offset}  key={key}")
    print("-" * 100)
    if isinstance(value, dict):
        print(json.dumps(value, indent=2, default=str))
    else:
        print(value)
    print()


def main():
    parser = argparse.ArgumentParser(description="Console consumer for the discovery-data Kafka topic")
    parser.add_argument(
        "--brokers",
        default="localhost:9092",
        help="Comma-separated Kafka/Redpanda broker addresses (default: localhost:9092)",
    )
    parser.add_argument(
        "--topic",
        default=DEFAULT_TOPIC,
        help=f"Topic to consume (default: {DEFAULT_TOPIC})",
    )
    parser.add_argument(
        "--group-id",
        default=None,
        help="Consumer group id. Omit for a throwaway group (reads all retained messages each run).",
    )
    parser.add_argument(
        "--latest-only",
        action="store_true",
        help="Only show new messages produced after the consumer starts.",
    )
    args = parser.parse_args()

    brokers = [b.strip() for b in args.brokers.split(",") if b.strip()]
    auto_offset_reset = "latest" if args.latest_only else "earliest"

    print(f"Connecting to Kafka/Redpanda brokers: {brokers}")
    print(f"Subscribing to topic: {args.topic}")
    print(f"Offset reset policy: {auto_offset_reset}")
    print("Press Ctrl+C to stop.\n")

    try:
        consumer = KafkaConsumer(
            args.topic,
            bootstrap_servers=brokers,
            auto_offset_reset=auto_offset_reset,
            enable_auto_commit=True,
            group_id=args.group_id,
            value_deserializer=lambda v: _safe_json(v),
            key_deserializer=lambda k: k.decode("utf-8") if k else None,
            consumer_timeout_ms=-1,
        )
    except KafkaError as exc:
        print(f"ERROR: could not connect to Kafka at {brokers}: {exc}", file=sys.stderr)
        sys.exit(1)

    try:
        for msg in consumer:
            pretty_print(msg.topic, msg.key, msg.value, msg.partition, msg.offset)
    except KeyboardInterrupt:
        print("\nStopping consumer.")
    finally:
        consumer.close()


def _safe_json(raw_bytes: bytes):
    if raw_bytes is None:
        return None
    try:
        return json.loads(raw_bytes.decode("utf-8"))
    except (json.JSONDecodeError, UnicodeDecodeError):
        return raw_bytes


if __name__ == "__main__":
    main()
