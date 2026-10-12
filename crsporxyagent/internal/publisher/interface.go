package publisher

import "context"

// EventPublisher publishes discovery events
type EventPublisher interface {
	Publish(ctx context.Context, topic string, event Event) error
	Close() error
}
