// Package bus is the event stream abstraction. Services only see these interfaces,
// so the Redis Streams adapter could be swapped for Kafka by writing one new file.
package bus

import (
	"context"
	"time"
)

// Message is one event: an ID assigned by the stream plus string fields.
type Message struct {
	ID     string
	Fields map[string]string
}

type Publisher interface {
	Publish(ctx context.Context, stream string, fields map[string]string) (string, error)
}

// Consumer reads from a stream through a consumer group: each message goes to one
// consumer in the group, stays "pending" until acknowledged, and can be reclaimed by
// another consumer if the first one dies. That is at-least-once delivery.
type Consumer interface {
	EnsureGroup(ctx context.Context, stream, group string) error
	Read(ctx context.Context, stream, group, consumer string, count int, block time.Duration) ([]Message, error)
	Ack(ctx context.Context, stream, group string, ids ...string) error
	Reclaim(ctx context.Context, stream, group, consumer string, minIdle time.Duration, count int) ([]Message, error)
}

// Subscriber tails a stream for every new message, with no group and no acks. Each
// gateway instance wants every event, so there is nothing to share between consumers.
type Subscriber interface {
	Subscribe(ctx context.Context, stream string, fn func(Message)) error
}
