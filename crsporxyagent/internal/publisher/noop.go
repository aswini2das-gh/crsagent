package publisher

import (
	"context"
	"sync"
)

// NoopPublisher is a no-op publisher that captures events for testing
type NoopPublisher struct {
	mu     sync.Mutex
	Events []Event
}

// NewNoopPublisher creates a new NoopPublisher
func NewNoopPublisher() *NoopPublisher {
	return &NoopPublisher{
		Events: make([]Event, 0),
	}
}

// Publish captures the event for later assertions
func (n *NoopPublisher) Publish(_ context.Context, _ string, event Event) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.Events = append(n.Events, event)
	return nil
}

// Close is a no-op
func (n *NoopPublisher) Close() error {
	return nil
}
