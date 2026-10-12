"""
CRS Telemetry Signing Gateway

Receives OTLP HTTP telemetry on port 4318, signs the payload with
HMAC-SHA256 using the key mounted at SIGNING_KEY_PATH, forwards it
to EXPORT_ENDPOINT, and publishes a copy to a Kafka topic so
downstream consumers can process the telemetry asynchronously.

When Kafka is unreachable or disabled the gateway still accepts and
forwards traffic — Kafka publishing is best-effort so the OTel
Collector pipeline never backs up.
"""

import base64
import hashlib
import hmac
import json
import logging
import os
import time
import threading

import requests
from flask import Flask, request, jsonify
from kafka import KafkaProducer
from kafka.errors import KafkaError

app = Flask(__name__)
logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
log = logging.getLogger("signing-gateway")

SIGNING_KEY_PATH = os.environ.get("SIGNING_KEY_PATH", "/etc/crs/signing/key")
SIGNING_ALGORITHM = os.environ.get("SIGNING_ALGORITHM", "hmac-sha256")
EXPORT_ENDPOINT = os.environ.get("EXPORT_ENDPOINT", "http://localhost:4319")
LISTEN_PORT = int(os.environ.get("LISTEN_PORT", "4318"))

# ── Kafka configuration ─────────────────────────────────────────────
KAFKA_ENABLED = os.environ.get("KAFKA_ENABLED", "false").lower() in ("true", "1", "yes")
KAFKA_BROKERS = os.environ.get("KAFKA_BROKERS", "localhost:9092")
KAFKA_TELEMETRY_TOPIC = os.environ.get("KAFKA_TELEMETRY_TOPIC", "telemetry-data")

_kafka_producer = None
_kafka_lock = threading.Lock()


def _get_kafka_producer():
    """Lazily initialise a shared KafkaProducer (thread-safe singleton)."""
    global _kafka_producer
    if not KAFKA_ENABLED:
        return None
    if _kafka_producer is not None:
        return _kafka_producer
    with _kafka_lock:
        if _kafka_producer is not None:
            return _kafka_producer
        brokers = [b.strip() for b in KAFKA_BROKERS.split(",") if b.strip()]
        try:
            _kafka_producer = KafkaProducer(
                bootstrap_servers=brokers,
                value_serializer=lambda v: json.dumps(v).encode("utf-8") if isinstance(v, dict) else v,
                acks="all",
                retries=3,
                retry_backoff_ms=500,
                request_timeout_ms=5000,
                max_block_ms=5000,
            )
            log.info("Kafka producer connected to %s", brokers)
        except KafkaError as exc:
            log.warning("Could not create Kafka producer (%s) — will retry on next request", exc)
        return _kafka_producer


def _publish_to_kafka(path: str, body: bytes, signature: str):
    """Best-effort publish of the telemetry payload to Kafka."""
    producer = _get_kafka_producer()
    if producer is None:
        return
    try:
        message = {
            "path": path,
            "signature": signature,
            "algorithm": SIGNING_ALGORITHM,
            "timestamp": int(time.time()),
            "payload_bytes": len(body),
            "payload_b64": base64.b64encode(body).decode("ascii"),
        }
        producer.send(
            KAFKA_TELEMETRY_TOPIC,
            value=message,
            key=path.encode("utf-8"),
        )
        log.debug("Queued telemetry to Kafka topic=%s path=%s (%d bytes)",
                  KAFKA_TELEMETRY_TOPIC, path, len(body))
    except KafkaError as exc:
        log.warning("Failed to publish to Kafka topic=%s: %s", KAFKA_TELEMETRY_TOPIC, exc)


def _load_signing_key() -> bytes:
    try:
        with open(SIGNING_KEY_PATH, "r") as f:
            return f.read().strip().encode()
    except FileNotFoundError:
        log.warning("Signing key not found at %s — using fallback key", SIGNING_KEY_PATH)
        return b"mock-signing-key-for-dev"


def _sign(payload: bytes, key: bytes) -> str:
    return hmac.new(key, payload, hashlib.sha256).hexdigest()


# ── Health / readiness ──────────────────────────────────────────────
@app.route("/healthz", methods=["GET"])
def healthz():
    kafka_status = "disabled"
    if KAFKA_ENABLED:
        kafka_status = "connected" if _kafka_producer is not None else "disconnected"
    return jsonify({
        "status": "ok",
        "kafka": kafka_status,
        "telemetry_topic": KAFKA_TELEMETRY_TOPIC,
    }), 200


# ── OTLP ingest (metrics, traces, logs) ─────────────────────────────
@app.route("/v1/metrics", methods=["POST"])
@app.route("/v1/traces", methods=["POST"])
@app.route("/v1/logs", methods=["POST"])
def ingest():
    body = request.get_data()
    key = _load_signing_key()
    signature = _sign(body, key)
    path = request.path  # e.g. /v1/metrics

    headers = {
        "Content-Type": request.content_type or "application/x-protobuf",
        "X-CRS-Signature": signature,
        "X-CRS-Algorithm": SIGNING_ALGORITHM,
        "X-CRS-Timestamp": str(int(time.time())),
    }

    # Publish to Kafka (best-effort, non-blocking).
    _publish_to_kafka(path, body, signature)

    # Forward to upstream.
    target = f"{EXPORT_ENDPOINT}{path}"
    try:
        resp = requests.post(target, data=body, headers=headers, timeout=5)
        log.info(
            "Forwarded %s (%d bytes) -> %s [%d]",
            path, len(body), target, resp.status_code,
        )
        return resp.content, resp.status_code, dict(resp.headers)
    except requests.exceptions.RequestException as exc:
        log.warning("Forward to %s failed: %s — accepting anyway", target, exc)
        return jsonify({"status": "accepted", "forwarded": False}), 202


# ── Catch-all for any other OTLP path ──────────────────────────────
@app.route("/", defaults={"path": ""}, methods=["POST"])
@app.route("/<path:path>", methods=["POST"])
def catch_all(path):
    body = request.get_data()
    log.info("Received POST /%s (%d bytes) — accepted (no forward rule)", path, len(body))
    return jsonify({"status": "accepted", "forwarded": False}), 202


if __name__ == "__main__":
    log.info("Starting Signing Gateway on :%d", LISTEN_PORT)
    log.info("  SIGNING_KEY_PATH      = %s", SIGNING_KEY_PATH)
    log.info("  EXPORT_ENDPOINT       = %s", EXPORT_ENDPOINT)
    log.info("  ALGORITHM             = %s", SIGNING_ALGORITHM)
    log.info("  KAFKA_ENABLED         = %s", KAFKA_ENABLED)
    log.info("  KAFKA_BROKERS         = %s", KAFKA_BROKERS)
    log.info("  KAFKA_TELEMETRY_TOPIC = %s", KAFKA_TELEMETRY_TOPIC)
    app.run(host="0.0.0.0", port=LISTEN_PORT)
