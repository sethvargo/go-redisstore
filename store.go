// Package redisstore defines a redis-backed storage system for limiting.
package redisstore

import (
	"context"
	"fmt"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/opencensus-integrations/redigo/redis"
	"github.com/sethvargo/go-limiter"
)

const (
	// hash field keys shared by the Lua script.
	fieldInterval  = "i"
	fieldMaxTokens = "m"
	fieldTokens    = "k"

	// weekSeconds is the number of seconds in a week.
	weekSeconds = 60 * 60 * 24 * 7

	// Common Redis commands
	cmdEXPIRE  = "EXPIRE"
	cmdHINCRBY = "HINCRBY"
	cmdHSET    = "HSET"
	cmdPING    = "PING"
)

var _ limiter.Store = (*store)(nil)

type store struct {
	tokens    uint64
	interval  time.Duration
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
			_, err := c.Do(cmdPING)
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

	luaScript := redis.NewScript(1, luaTemplate)

	s := &store{
		tokens:    tokens,
		interval:  interval,
		pool:      pool,
		luaScript: luaScript,
	}
	return s, nil
}

// Take attempts to remove a token from the named key. If the take is
// successful, it returns true, otherwise false. It also returns the configured
// limit, remaining tokens, and reset time, if one was found. Any errors
// connecting to the store or parsing the return value are considered failures
// and fail the take.
func (s *store) Take(ctx context.Context, key string) (limit uint64, remaining uint64, next uint64, ok bool, retErr error) {
	// If the store is stopped, all requests are rejected.
	if atomic.LoadUint32(&s.stopped) == 1 {
		retErr = limiter.ErrStopped
		return
	}

	// Get the current time, since this is when the function was called, and we
	// want to limit from call time, not invoke time.
	now := uint64(time.Now().UTC().UnixNano())

	// Get a client from the pool.
	conn, ok := s.pool.GetWithContext(ctx).(redis.ConnWithContext)
	if !ok {
		return 0, 0, 0, false, fmt.Errorf("pool is not a ConnWithContext")
	}
	if err := conn.Err(); err != nil {
		retErr = fmt.Errorf("connection is not usable: %w", err)
		return
	}
	defer closeConnection(ctx, conn, &retErr)

	nowStr := strconv.FormatUint(now, 10)
	tokensStr := strconv.FormatUint(s.tokens, 10)
	intervalStr := strconv.FormatInt(s.interval.Nanoseconds(), 10)
	a, err := redis.Int64s(s.luaScript.DoContext(ctx, conn, key, nowStr, tokensStr, intervalStr))
	if err != nil {
		retErr = fmt.Errorf("failed to run script: %w", err)
		return
	}

	if len(a) < 4 {
		retErr = fmt.Errorf("response has less than 4 values: %#v", a)
		return
	}

	limit, remaining, next, ok = uint64(a[0]), uint64(a[1]), uint64(a[2]), a[3] == 1
	return
}

// Set sets the key's limit to the provided value and interval.
func (s *store) Set(ctx context.Context, key string, tokens uint64, interval time.Duration) (retErr error) {
	// If the store is stopped, all requests are rejected.
	if atomic.LoadUint32(&s.stopped) == 1 {
		retErr = limiter.ErrStopped
		return
	}

	// Get a client from the pool.
	conn, ok := s.pool.GetWithContext(ctx).(redis.ConnWithContext)
	if !ok {
		retErr = fmt.Errorf("pool is not a ConnWithContext")
		return
	}
	if err := conn.Err(); err != nil {
		retErr = fmt.Errorf("connection is not usable: %w", err)
		return
	}
	defer closeConnection(ctx, conn, &retErr)

	// Set configuration on the key.
	tokensStr := strconv.FormatUint(tokens, 10)
	intervalStr := strconv.FormatInt(interval.Nanoseconds(), 10)
	if err := conn.SendContext(ctx, cmdHSET, key,
		fieldTokens, tokensStr,
		fieldMaxTokens, tokensStr,
		fieldInterval, intervalStr,
	); err != nil {
		retErr = fmt.Errorf("failed to set key: %w", err)
		return
	}

	// Set the key to expire. This will prevent a leak when a key's configuration
	// is set, but nothing is ever taken from the bucket.
	if err := conn.SendContext(ctx, cmdEXPIRE, key, weekSeconds); err != nil {
		retErr = fmt.Errorf("failed to set expire on key: %w", err)
		return
	}

	return
}

// Burst adds the given tokens to the key's bucket.
func (s *store) Burst(ctx context.Context, key string, tokens uint64) (retErr error) {
	// If the store is stopped, all requests are rejected.
	if atomic.LoadUint32(&s.stopped) == 1 {
		retErr = limiter.ErrStopped
		return
	}

	// Get a client from the pool.
	conn, ok := s.pool.GetWithContext(ctx).(redis.ConnWithContext)
	if !ok {
		retErr = fmt.Errorf("pool is not a ConnWithContext")
		return
	}
	if err := conn.Err(); err != nil {
		retErr = fmt.Errorf("connection is not usable: %w", err)
		return
	}
	defer closeConnection(ctx, conn, &retErr)

	// Set configuration on the key.
	tokensStr := strconv.FormatUint(tokens, 10)
	if err := conn.Send(cmdHINCRBY, key, fieldTokens, tokensStr); err != nil {
		retErr = fmt.Errorf("failed to set key: %w", err)
		return
	}

	// Set the key to expire. This will prevent a leak when a key's configuration
	// is set, but nothing is ever taken from the bucket.
	if err := conn.Send(cmdEXPIRE, key, weekSeconds); err != nil {
		retErr = fmt.Errorf("failed to set expire on key: %w", err)
		return
	}

	return
}

// Close stops the memory limiter and cleans up any outstanding sessions. You
// should always call CloseWithContext() as it releases any open network
// connections.
func (s *store) Close(_ context.Context) error {
	if !atomic.CompareAndSwapUint32(&s.stopped, 0, 1) {
		return nil
	}

	// Close the connection pool.
	if err := s.pool.Close(); err != nil {
		return fmt.Errorf("failed to close pool: %w", err)
	}
	return nil
}

// closeConnection is a helper for closing the connection object. It is used in
// defer statements to alter the provided error pointer before the final result
// is bubbled up the stack.
func closeConnection(ctx context.Context, c redis.ConnWithContext, err *error) {
	nerr := c.CloseContext(ctx)
	if *err == nil {
		*err = nerr
	}
}
