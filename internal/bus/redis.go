package bus

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/trace"

	"ainori/internal/telemetry"
)

// maxLen caps every stream. "~" lets Redis trim in whole blocks, which is cheaper.
const maxLen = 10000

type RedisPublisher struct {
	rdb *redis.Client
}

func NewRedisPublisher(rdb *redis.Client) *RedisPublisher {
	return &RedisPublisher{rdb: rdb}
}

func (p *RedisPublisher) Publish(ctx context.Context, stream string, fields map[string]string) (string, error) {
	ctx, span := telemetry.Tracer().Start(ctx, "publish "+stream, trace.WithSpanKind(trace.SpanKindProducer))
	defer span.End()

	// Copy so the caller's map is untouched, then add the trace context so the
	// consumer can continue this trace.
	carried := make(map[string]string, len(fields)+2)
	for k, v := range fields {
		carried[k] = v
	}
	telemetry.Inject(ctx, carried)
	values := make(map[string]any, len(carried))
	for k, v := range carried {
		values[k] = v
	}
	id, err := p.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: stream,
		MaxLen: maxLen,
		Approx: true,
		Values: values,
	}).Result()
	if err != nil {
		return "", fmt.Errorf("xadd %s: %w", stream, err)
	}
	return id, nil
}

type RedisConsumer struct {
	rdb *redis.Client
}

func NewRedisConsumer(rdb *redis.Client) *RedisConsumer {
	return &RedisConsumer{rdb: rdb}
}

// EnsureGroup creates the stream and group if missing. Starting at "0" means requests
// published before the matcher first started are still picked up.
func (c *RedisConsumer) EnsureGroup(ctx context.Context, stream, group string) error {
	err := c.rdb.XGroupCreateMkStream(ctx, stream, group, "0").Err()
	if err != nil && !strings.HasPrefix(err.Error(), "BUSYGROUP") {
		return fmt.Errorf("create group %s on %s: %w", group, stream, err)
	}
	return nil
}

func (c *RedisConsumer) Read(ctx context.Context, stream, group, consumer string, count int, block time.Duration) ([]Message, error) {
	res, err := c.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    group,
		Consumer: consumer,
		Streams:  []string{stream, ">"},
		Count:    int64(count),
		Block:    block,
	}).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("xreadgroup %s: %w", stream, err)
	}
	var out []Message
	for _, s := range res {
		out = append(out, toMessages(s.Messages)...)
	}
	return out, nil
}

func (c *RedisConsumer) Ack(ctx context.Context, stream, group string, ids ...string) error {
	if err := c.rdb.XAck(ctx, stream, group, ids...).Err(); err != nil {
		return fmt.Errorf("xack %s: %w", stream, err)
	}
	return nil
}

// Reclaim takes over messages that another consumer read but never acknowledged for minIdle.
func (c *RedisConsumer) Reclaim(ctx context.Context, stream, group, consumer string, minIdle time.Duration, count int) ([]Message, error) {
	msgs, _, err := c.rdb.XAutoClaim(ctx, &redis.XAutoClaimArgs{
		Stream:   stream,
		Group:    group,
		Consumer: consumer,
		MinIdle:  minIdle,
		Start:    "0",
		Count:    int64(count),
	}).Result()
	if err != nil {
		return nil, fmt.Errorf("xautoclaim %s: %w", stream, err)
	}
	return toMessages(msgs), nil
}

func toMessages(in []redis.XMessage) []Message {
	out := make([]Message, 0, len(in))
	for _, m := range in {
		f := make(map[string]string, len(m.Values))
		for k, v := range m.Values {
			f[k] = fmt.Sprint(v)
		}
		out = append(out, Message{ID: m.ID, Fields: f})
	}
	return out
}

// Subscribe calls fn for each message added after the call starts, until ctx is cancelled.
func (c *RedisConsumer) Subscribe(ctx context.Context, stream string, fn func(Message)) error {
	// Resolve "now" to a concrete ID first. XREAD with "$" would miss anything added
	// between two blocking calls.
	last := "0-0"
	if latest, err := c.rdb.XRevRangeN(ctx, stream, "+", "-", 1).Result(); err == nil && len(latest) == 1 {
		last = latest[0].ID
	}
	for ctx.Err() == nil {
		res, err := c.rdb.XRead(ctx, &redis.XReadArgs{Streams: []string{stream, last}, Count: 200, Block: time.Second}).Result()
		if err == redis.Nil {
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			time.Sleep(500 * time.Millisecond)
			continue
		}
		for _, s := range res {
			for _, m := range toMessages(s.Messages) {
				last = m.ID
				fn(m)
			}
		}
	}
	return nil
}
