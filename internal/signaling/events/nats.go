package events

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	// NOTE: Uncomment when adding NATS dependency
	// "encoding/json"
	// "sync"
	// "github.com/nats-io/nats.go"
	// "github.com/nats-io/nats.go/jetstream"
)

// NATSPublisher publishes events to NATS JetStream.
// This is a sketch implementation - uncomment NATS imports to activate.
// The fields are commented out since NATS is not compiled in; they exist
// in the commented-out implementation block below.
type NATSPublisher struct {
	// NOTE: Fields are defined in the commented-out implementation.
	// When enabling NATS, uncomment the imports and implementation,
	// and add the following fields:
	//   js         jetstream.JetStream
	//   conn       *nats.Conn
	//   streamName string
	//   logger     *slog.Logger
	//   asyncCh    chan Event
	//   asyncWg    sync.WaitGroup
	//   closedMu   sync.RWMutex
	//   closed     bool
	//   mu         sync.Mutex
	//   publishCount  int64
	//   errorCount    int64
	//   asyncDropped  int64
}

// NATSConfig configures the NATS publisher.
type NATSConfig struct {
	// NATS server URL(s), comma-separated
	URL string
	// Stream name for call events
	StreamName string
	// Subject prefix (default: "switchboard")
	SubjectPrefix string
	// Async buffer size (default: 10000)
	AsyncBufferSize int
	// Connection timeout
	ConnectTimeout time.Duration
	// Reconnect settings
	MaxReconnects     int
	ReconnectWait     time.Duration
	ReconnectJitter   time.Duration
	// TLS settings
	TLSCertFile string
	TLSKeyFile  string
	TLSCAFile   string
	// Auth
	NKeyFile   string
	CredsFile  string
	Token      string
	User       string
	Password   string
}

// DefaultNATSConfig returns sensible defaults for VoIP workloads.
func DefaultNATSConfig() NATSConfig {
	return NATSConfig{
		URL:             "nats://localhost:4222",
		StreamName:      "SWITCHBOARD_CALLS",
		SubjectPrefix:   "switchboard",
		AsyncBufferSize: 10000,
		ConnectTimeout:  5 * time.Second,
		MaxReconnects:   -1, // Infinite
		ReconnectWait:   2 * time.Second,
		ReconnectJitter: 500 * time.Millisecond,
	}
}

// StreamConfig returns the JetStream stream configuration.
// This is the recommended config for call event streams.
func StreamConfig(name string) map[string]interface{} {
	return map[string]interface{}{
		"name": name,
		// Capture all call-related subjects
		"subjects": []string{
			"switchboard.calls.>",
		},
		// Limits retention policy - keep events for 7 days
		"retention": "limits",
		"max_age":   7 * 24 * time.Hour,
		// Discard old messages when limits reached
		"discard": "old",
		// Storage type
		"storage": "file",
		// Replicas for HA (adjust based on cluster size)
		"num_replicas": 1,
		// Enable duplicate detection (5 minute window)
		"duplicate_window": 5 * time.Minute,
		// Allow rollup for state compaction
		"allow_rollup_hdrs": true,
		// Deny delete/purge for audit compliance
		"deny_delete": false,
		"deny_purge":  false,
	}
}

// ConsumerConfig returns recommended consumer configurations.
func ConsumerConfigs() map[string]map[string]interface{} {
	return map[string]map[string]interface{}{
		// CDR processor - needs all events, durable, exactly-once
		"cdr-processor": {
			"durable_name":      "cdr-processor",
			"filter_subject":    "switchboard.calls.>",
			"ack_policy":        "explicit",
			"ack_wait":          30 * time.Second,
			"max_deliver":       5, // Retry failed processing
			"max_ack_pending":   1000,
			"deliver_policy":    "all",
			"replay_policy":     "instant",
			"sample_freq":       "100%",
		},
		// Real-time dashboard - ephemeral, latest only
		"dashboard": {
			"filter_subject": "switchboard.calls.>",
			"ack_policy":     "none",
			"deliver_policy": "new",
			"replay_policy":  "instant",
			// No durable_name = ephemeral
		},
		// Billing - specific events only
		"billing": {
			"durable_name":   "billing",
			"filter_subject": "switchboard.calls.*.ended",
			"ack_policy":     "explicit",
			"ack_wait":       60 * time.Second,
			"max_deliver":    10, // Critical, retry more
			"deliver_policy": "all",
		},
	}
}

/*
// NewNATSPublisher creates a NATS JetStream publisher.
// Uncomment when adding NATS dependency.
func NewNATSPublisher(cfg NATSConfig, logger *slog.Logger) (*NATSPublisher, error) {
	if logger == nil {
		logger = slog.Default()
	}

	// Build connection options
	opts := []nats.Option{
		nats.Name("switchboard-events"),
		nats.Timeout(cfg.ConnectTimeout),
		nats.MaxReconnects(cfg.MaxReconnects),
		nats.ReconnectWait(cfg.ReconnectWait),
		nats.ReconnectJitter(cfg.ReconnectJitter, cfg.ReconnectJitter),
		nats.DisconnectErrHandler(func(nc *nats.Conn, err error) {
			logger.Warn("NATS disconnected", "error", err)
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			logger.Info("NATS reconnected", "url", nc.ConnectedUrl())
		}),
		nats.ErrorHandler(func(nc *nats.Conn, sub *nats.Subscription, err error) {
			logger.Error("NATS error", "error", err)
		}),
	}

	// Add auth
	if cfg.CredsFile != "" {
		opts = append(opts, nats.UserCredentials(cfg.CredsFile))
	} else if cfg.NKeyFile != "" {
		opt, err := nats.NkeyOptionFromSeed(cfg.NKeyFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load NKey: %w", err)
		}
		opts = append(opts, opt)
	} else if cfg.Token != "" {
		opts = append(opts, nats.Token(cfg.Token))
	} else if cfg.User != "" {
		opts = append(opts, nats.UserInfo(cfg.User, cfg.Password))
	}

	// Add TLS
	if cfg.TLSCertFile != "" {
		opts = append(opts, nats.ClientCert(cfg.TLSCertFile, cfg.TLSKeyFile))
	}
	if cfg.TLSCAFile != "" {
		opts = append(opts, nats.RootCAs(cfg.TLSCAFile))
	}

	// Connect
	conn, err := nats.Connect(cfg.URL, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to NATS: %w", err)
	}

	// Create JetStream context
	js, err := jetstream.New(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to create JetStream context: %w", err)
	}

	// Ensure stream exists
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	streamCfg := jetstream.StreamConfig{
		Name:            cfg.StreamName,
		Subjects:        []string{"switchboard.calls.>"},
		Retention:       jetstream.LimitsPolicy,
		MaxAge:          7 * 24 * time.Hour,
		Storage:         jetstream.FileStorage,
		Replicas:        1,
		DuplicateWindow: 5 * time.Minute,
	}

	_, err = js.CreateOrUpdateStream(ctx, streamCfg)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to create stream: %w", err)
	}

	bufSize := cfg.AsyncBufferSize
	if bufSize <= 0 {
		bufSize = 10000
	}

	p := &NATSPublisher{
		js:         js,
		conn:       conn,
		streamName: cfg.StreamName,
		logger:     logger,
		asyncCh:    make(chan Event, bufSize),
	}

	// Start async publisher goroutine
	p.asyncWg.Add(1)
	go p.asyncPublisher()

	logger.Info("NATS publisher initialized",
		"url", cfg.URL,
		"stream", cfg.StreamName,
	)

	return p, nil
}

func (p *NATSPublisher) asyncPublisher() {
	defer p.asyncWg.Done()
	for event := range p.asyncCh {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := p.Publish(ctx, event); err != nil {
			p.logger.Warn("async publish failed",
				"error", err,
				"type", event.Type(),
				"call_id", event.CallID(),
			)
		}
		cancel()
	}
}

func (p *NATSPublisher) Publish(ctx context.Context, event Event) error {
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal event: %w", err)
	}

	subject := event.Subject()

	// Use EventID for deduplication
	msgID := ""
	if be, ok := event.(*BaseEvent); ok {
		msgID = be.EventID
	}

	opts := []jetstream.PublishOpt{}
	if msgID != "" {
		opts = append(opts, jetstream.WithMsgID(msgID))
	}

	ack, err := p.js.Publish(ctx, subject, data, opts...)
	if err != nil {
		p.mu.Lock()
		p.errorCount++
		p.mu.Unlock()
		return fmt.Errorf("failed to publish to %s: %w", subject, err)
	}

	p.mu.Lock()
	p.publishCount++
	p.mu.Unlock()

	p.logger.Debug("event published",
		"subject", subject,
		"stream", ack.Stream,
		"seq", ack.Sequence,
	)

	return nil
}

func (p *NATSPublisher) PublishAsync(event Event) {
	p.closedMu.RLock()
	if p.closed {
		p.closedMu.RUnlock()
		return
	}
	p.closedMu.RUnlock()

	select {
	case p.asyncCh <- event:
	default:
		p.mu.Lock()
		p.asyncDropped++
		p.mu.Unlock()
		p.logger.Warn("async publish buffer full, event dropped",
			"type", event.Type(),
			"call_id", event.CallID(),
		)
	}
}

func (p *NATSPublisher) Flush(ctx context.Context) error {
	// Drain async channel
	p.closedMu.Lock()
	p.closed = true
	p.closedMu.Unlock()
	close(p.asyncCh)
	p.asyncWg.Wait()

	// Flush NATS connection
	return p.conn.FlushWithContext(ctx)
}

func (p *NATSPublisher) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := p.Flush(ctx); err != nil {
		p.logger.Warn("flush failed during close", "error", err)
	}

	p.conn.Close()
	return nil
}

func (p *NATSPublisher) Stats() (published, errors, asyncDropped int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.publishCount, p.errorCount, p.asyncDropped
}
*/

// Placeholder for when NATS is not compiled in
func NewNATSPublisher(cfg NATSConfig, logger *slog.Logger) (*NATSPublisher, error) {
	return nil, fmt.Errorf("NATS support not compiled in - uncomment nats.go imports")
}

func (p *NATSPublisher) Publish(ctx context.Context, event Event) error {
	return fmt.Errorf("NATS support not compiled in")
}

func (p *NATSPublisher) PublishAsync(event Event) {}

func (p *NATSPublisher) Flush(ctx context.Context) error {
	return nil
}

func (p *NATSPublisher) Close() error {
	return nil
}
