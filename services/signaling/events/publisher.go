package events

import (
	"context"
	"log/slog"
	"sync"
)

// Publisher is the interface for publishing call events.
// Implementations may be no-op, logging, in-memory (for testing),
// or NATS JetStream for production.
type Publisher interface {
	// Publish sends an event. Returns error only for transport failures,
	// not for invalid events (those should be caught at construction).
	Publish(ctx context.Context, event Event) error

	// PublishAsync sends an event without waiting for confirmation.
	// For high-throughput scenarios where some loss is acceptable.
	PublishAsync(event Event)

	// Flush ensures all pending async events are published.
	// Call before shutdown to avoid event loss.
	Flush(ctx context.Context) error

	// Close releases resources. Calls Flush internally.
	Close() error
}

// Subscriber is the interface for receiving events.
// Used for testing and local event processing.
type Subscriber interface {
	// Subscribe returns a channel of events matching the subject pattern.
	// Pattern supports wildcards: * (single token), > (remaining tokens)
	Subscribe(ctx context.Context, pattern string) (<-chan Event, error)

	// Close stops all subscriptions.
	Close() error
}

// NoopPublisher discards all events. Use when NATS is not configured.
type NoopPublisher struct{}

// NewNoopPublisher creates a publisher that silently discards events.
func NewNoopPublisher() *NoopPublisher {
	return &NoopPublisher{}
}

func (p *NoopPublisher) Publish(ctx context.Context, event Event) error {
	return nil
}

func (p *NoopPublisher) PublishAsync(event Event) {}

func (p *NoopPublisher) Flush(ctx context.Context) error {
	return nil
}

func (p *NoopPublisher) Close() error {
	return nil
}

// LoggingPublisher logs events at debug level. Useful for development.
type LoggingPublisher struct {
	logger *slog.Logger
}

// NewLoggingPublisher creates a publisher that logs events.
func NewLoggingPublisher(logger *slog.Logger) *LoggingPublisher {
	if logger == nil {
		logger = slog.Default()
	}
	return &LoggingPublisher{logger: logger}
}

func (p *LoggingPublisher) Publish(ctx context.Context, event Event) error {
	p.logger.Debug("event published",
		"subject", event.Subject(),
		"type", event.Type(),
		"call_id", event.CallID(),
		"timestamp", event.Timestamp(),
	)
	return nil
}

func (p *LoggingPublisher) PublishAsync(event Event) {
	p.logger.Debug("event published (async)",
		"subject", event.Subject(),
		"type", event.Type(),
		"call_id", event.CallID(),
	)
}

func (p *LoggingPublisher) Flush(ctx context.Context) error {
	return nil
}

func (p *LoggingPublisher) Close() error {
	return nil
}

// ChannelPublisher publishes to an in-memory channel. Used for testing
// and for local event processing (e.g., CDR generation).
type ChannelPublisher struct {
	mu       sync.RWMutex
	ch       chan Event
	bufSize  int
	closed   bool
	dropCount int64
}

// NewChannelPublisher creates a publisher backed by a buffered channel.
// Events are dropped if the buffer is full (with warning logged).
func NewChannelPublisher(bufferSize int) *ChannelPublisher {
	if bufferSize <= 0 {
		bufferSize = 1000
	}
	return &ChannelPublisher{
		ch:      make(chan Event, bufferSize),
		bufSize: bufferSize,
	}
}

func (p *ChannelPublisher) Publish(ctx context.Context, event Event) error {
	p.mu.RLock()
	if p.closed {
		p.mu.RUnlock()
		return nil
	}
	p.mu.RUnlock()

	select {
	case p.ch <- event:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
		// Buffer full, drop event
		p.mu.Lock()
		p.dropCount++
		p.mu.Unlock()
		slog.Warn("event dropped: buffer full",
			"type", event.Type(),
			"call_id", event.CallID(),
		)
		return nil
	}
}

func (p *ChannelPublisher) PublishAsync(event Event) {
	p.mu.RLock()
	if p.closed {
		p.mu.RUnlock()
		return
	}
	p.mu.RUnlock()

	select {
	case p.ch <- event:
	default:
		p.mu.Lock()
		p.dropCount++
		p.mu.Unlock()
	}
}

func (p *ChannelPublisher) Flush(ctx context.Context) error {
	return nil // Channel is always "flushed"
}

func (p *ChannelPublisher) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.closed {
		p.closed = true
		close(p.ch)
	}
	return nil
}

// Events returns the channel for consuming events.
func (p *ChannelPublisher) Events() <-chan Event {
	return p.ch
}

// DroppedCount returns the number of events dropped due to buffer overflow.
func (p *ChannelPublisher) DroppedCount() int64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.dropCount
}

// MultiPublisher fans out events to multiple publishers.
// Useful for sending events to both NATS and a local CDR processor.
type MultiPublisher struct {
	publishers []Publisher
}

// NewMultiPublisher creates a publisher that sends to all provided publishers.
func NewMultiPublisher(publishers ...Publisher) *MultiPublisher {
	return &MultiPublisher{publishers: publishers}
}

func (p *MultiPublisher) Publish(ctx context.Context, event Event) error {
	var lastErr error
	for _, pub := range p.publishers {
		if err := pub.Publish(ctx, event); err != nil {
			lastErr = err
			slog.Warn("multi-publisher: one publisher failed",
				"error", err,
				"type", event.Type(),
			)
		}
	}
	return lastErr
}

func (p *MultiPublisher) PublishAsync(event Event) {
	for _, pub := range p.publishers {
		pub.PublishAsync(event)
	}
}

func (p *MultiPublisher) Flush(ctx context.Context) error {
	var lastErr error
	for _, pub := range p.publishers {
		if err := pub.Flush(ctx); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

func (p *MultiPublisher) Close() error {
	var lastErr error
	for _, pub := range p.publishers {
		if err := pub.Close(); err != nil {
			lastErr = err
		}
	}
	return lastErr
}
