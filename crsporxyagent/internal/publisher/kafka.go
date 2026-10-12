package publisher

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"

	"github.com/go-logr/logr"
	"github.com/segmentio/kafka-go"
	"github.com/segmentio/kafka-go/sasl/scram"
)

// Config holds the configuration for the Kafka publisher
type Config struct {
	Brokers    []string `json:"brokers"`
	Username   string   `json:"username"`
	Password   string   `json:"password"`
	TLSEnabled bool     `json:"tlsEnabled"`
	RateLimit  int      `json:"rateLimit"`
}

// KafkaPublisher publishes discovery events to Kafka
type KafkaPublisher struct {
	writer *kafka.Writer
	logger logr.Logger
}

// NewKafkaPublisher creates a new KafkaPublisher with the given config.
// SASL authentication is only configured when a username is supplied, and TLS
// is only configured when explicitly enabled -- this lets the publisher talk
// to an unauthenticated local/dev Kafka broker (e.g. a lightweight Kafka or
// Redpanda running in Docker Desktop) as well as a secured remote cluster.
// An empty-credential SASL mechanism is still a non-nil value, so it must
// never be set unconditionally: a broker with SASL disabled (like a local
// dev Redpanda) will reject the handshake as an illegal SASL state.
func NewKafkaPublisher(cfg Config, logger logr.Logger) (*KafkaPublisher, error) {
	transport := &kafka.Transport{}

	if cfg.Username != "" {
		mechanism, err := scram.Mechanism(scram.SHA256, cfg.Username, cfg.Password)
		if err != nil {
			return nil, fmt.Errorf("creating SASL mechanism: %w", err)
		}
		transport.SASL = mechanism
	}

	if cfg.TLSEnabled {
		transport.TLS = &tls.Config{
			InsecureSkipVerify: false,
		}
	}

	writer := &kafka.Writer{
		Addr:         kafka.TCP(cfg.Brokers...),
		Balancer:     &kafka.LeastBytes{},
		RequiredAcks: kafka.RequireAll,
		Async:        false,
		Compression:  kafka.Snappy,
		Transport:    transport,
	}

	logger.Info("kafka publisher initialized", "brokers", cfg.Brokers, "tlsEnabled", cfg.TLSEnabled)

	return &KafkaPublisher{
		writer: writer,
		logger: logger,
	}, nil
}

// Publish marshals the event to JSON and writes it to the given Kafka topic
func (kp *KafkaPublisher) Publish(ctx context.Context, topic string, event Event) error {
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshaling event: %w", err)
	}

	msg := kafka.Message{
		Topic: topic,
		Key:   []byte(event.NodeUID),
		Value: data,
	}

	if err := kp.writer.WriteMessages(ctx, msg); err != nil {
		kp.logger.Error(err, "failed to publish event",
			"topic", topic,
			"nodeUID", event.NodeUID,
			"eventType", event.EventType,
		)
		return fmt.Errorf("writing kafka message: %w", err)
	}

	kp.logger.V(1).Info("event published",
		"topic", topic,
		"nodeUID", event.NodeUID,
		"eventType", event.EventType,
		"revision", event.Revision,
	)

	return nil
}

// Close closes the underlying Kafka writer
func (kp *KafkaPublisher) Close() error {
	return kp.writer.Close()
}

// TopicForCluster returns the Kafka topic name for a given cluster ID
func TopicForCluster(clusterID string) string {
	return "crs.discovery.nodes." + clusterID
}

// DefaultDiscoveryTopic is used when no topic is configured via Helm values
// (kafka.discoveryTopic) / the DISCOVERY_TOPIC env var.
const DefaultDiscoveryTopic = "discovery-data"
