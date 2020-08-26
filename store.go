// Package redisstore defines a redis-backed storage system for limiting.
package redisstore

import (
	"context"
	"fmt"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/gomodule/redigo/redis"
	"github.com/sethvargo/go-limiter"
)

var _ limiter.Store = (*store)(nil)
var _ limiter.StoreWithContext = (*store)(nil)

type store struct {
	tokens    uint64
	interval  time.Duration
	rate      float64
	ttl       uint64
	pool      *redis.Pool
	luaScript *redis.Script

	stopped uint32
}

// Config is used as input to New. It defines the behavior of the storage
// system.
type Config struct {
	// Tokens is the number of tokens to allow per interval. The default value is
	// 1.
	Tokens uint64

	// Interval is the time interval upon which to enforce rate limiting. The
	// default value is 1 second.
	Interval time.Duration

	// TTL is the amount of time a key should exist without changes before
	// purging. The default is 10 x interval.
	TTL uint64

	// Dial is the function to use as the dialer. This is ignored when used with
	// NewWithPool.
	Dial func() (redis.Conn, error)
}

// New uses a Redis instance to back a rate limiter that to limit the number of
// permitted events over an interval.
func New(c *Config) (limiter.Store, error) {
	return NewWithPool(c, &redis.Pool{
		MaxActive:   100,
		IdleTimeout: 5 * time.Minute,
		Dial:        c.Dial,
		TestOnBorrow: func(c redis.Conn, _ time.Time) error {
			_, err := c.Do("PING")
			return err
		},
	})
}

// NewWithPool creates a new limiter using the given redis pool. Use this to
// customize lower-level details about the pool.
func NewWithPool(c *Config, pool *redis.Pool) (limiter.Store, error) {
	if c == nil {
		c = new(Config)
	}

	tokens := uint64(1)
	if c.Tokens > 0 {
		tokens = c.Tokens
	}

	interval := 1 * time.Second
	if c.Interval > 0 {
		interval = c.Interval
	}

	rate := float64(interval) / float64(tokens)

	ttl := 10 * uint64(interval.Seconds())
	if c.TTL > 0 {
		ttl = c.TTL
	}
	if ttl == 0 {
		return nil, fmt.Errorf("ttl cannot be 0")
	}

	luaScript := redis.NewScript(1, fmt.Sprintf(luaTemplate, tokens, interval, rate, ttl))

	s := &store{
		tokens:    tokens,
		interval:  interval,
		rate:      rate,
		ttl:       ttl,
		pool:      pool,
		luaScript: luaScript,
	}
	return s, nil
}

// Take attempts to remove a token from the named key. See TakeWithContext for
// more information.
func (s *store) Take(key string) (tokens uint64, remaining uint64, next uint64, ok bool, retErr error) {
	return s.TakeWithContext(context.Background(), key)
}

// TakeWithContext attempts to remove a token from the named key. If the take is
// successful, it returns true, otherwise false. It also returns the configured
// limit, remaining tokens, and reset time, if one was found. Any errors
// connecting to the store or parsing the return value are considered failures
// and fail the take.
func (s *store) TakeWithContext(ctx context.Context, key string) (tokens uint64, remaining uint64, next uint64, ok bool, retErr error) {
	// If the store is stopped, all requests are rejected.
	if atomic.LoadUint32(&s.stopped) == 1 {
		return 0, 0, 0, false, limiter.ErrStopped
	}

	// Get a client from the pool.
	conn := s.pool.Get()
	if err := conn.Err(); err != nil {
		return 0, 0, 0, false, fmt.Errorf("connection is not usable: %w", err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			retErr = fmt.Errorf("failed to close connection: %v, original error: %w", err, retErr)
		}
	}()

	now := uint64(time.Now().UTC().UnixNano())
	nowStr := strconv.FormatUint(now, 10)

	a, err := redis.Int64s(s.luaScript.Do(conn, key, nowStr))
	if err != nil {
		retErr = fmt.Errorf("failed to EVAL script: %w", err)
		return 0, 0, 0, false, retErr
	}

	if len(a) < 3 {
		retErr = fmt.Errorf("response has less than 3 values: %#v", a)
		return 0, 0, 0, false, retErr
	}

	tokens, next, ok = uint64(a[0]), uint64(a[1]), a[2] == 1
	return s.tokens, tokens, next, ok, nil
}

// Close stops the limiter. See CloseWithContext for more information.
func (s *store) Close() error {
	if !atomic.CompareAndSwapUint32(&s.stopped, 0, 1) {
		return nil
	}

	// Close the connection pool.
	return s.pool.Close()
}

// CloseWithContext stops the memory limiter and cleans up any outstanding
// sessions. You should always call CloseWithContext() as it releases any open
// network connections.
func (s *store) CloseWithContext(_ context.Context) error {
	if !atomic.CompareAndSwapUint32(&s.stopped, 0, 1) {
		return nil
	}

	// Close the connection pool.
	return s.pool.Close()
}
