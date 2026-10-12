#!/usr/bin/env python3
"""
Console consumer for the CRSProxyAgent "telemetry-data" Kafka topic.

Reads telemetry messages published by the Signing Gateway (signed OTLP
payloads forwarded from the OTel Collector) and pretty-prints them.
The raw OTLP payload is base64-encoded in the message, so by default
this script shows its size rather than dumping the raw bytes; pass
--show-payload to print the decoded base64 payload bytes as well.

Usage:
    python telemetry_consumer.py
    python telemetry_consumer.py --brokers localhost:9092
    python telemetry_consumer.py --topic telemetry-data --latest-only
    python telemetry_consumer.py --show-payload
"""

import argparse
import base64
import json
import sys
from datetime import datetime, timezone

from kafka import KafkaConsumer
from kafka.errors import KafkaError

DEFAULT_TOPIC = "telemetry-data"


def pretty_print(topic: str, key, value, partition: int, offset: int, show_payload: bool):
    ts = datetime.now(timezone.utc).strftime("%Y-%m-%d %H:%M:%S UTC")
    print("=" * 100)
    print(f"[{ts}]  topic={topic}  partition={partition}  offset={offset}  key={key}")
    print("-" * 100)

    if isinstance(value, dict):
        display = dict(value)
        if "payload_b64" in display:
            raw = base64.b64decode(display["payload_b64"]) if display["payload_b64"] else b""
            display["payload_b64"] = f"<{len(raw)} bytes, base64-encoded>"
            print(json.dumps(display, indent=2, default=str))
            if show_payload:
                print("-" * 100)
                print("Decoded payload (raw bytes):")
                print(raw)
        else:
            print(json.dumps(display, indent=2, default=str))
    else:
        print(value)
    print()


def main():
    parser = argparse.ArgumentParser(description="Console consumer for the telemetry-data Kafka topic")
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
    parser.add_argument(
        "--show-payload",
        action="store_true",
        help="Also print the decoded raw OTLP payload bytes (can be noisy/binary).",
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
            pretty_print(msg.topic, msg.key, msg.value, msg.partition, msg.offset, args.show_payload)
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
