package redisstore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/gomodule/redigo/redis"
)

func testKey(tb testing.TB) string {
	tb.Helper()

	var b [512]byte
	if _, err := rand.Read(b[:]); err != nil {
		tb.Fatalf("failed to generate random string: %v", err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(b[:]))
	return digest[:32]
}

// testStore returns a store with the given defaults. The store is closed when
// the test ends.
func testStore(tb testing.TB, tokens uint64, interval time.Duration) *Store {
	tb.Helper()

	host := os.Getenv("REDIS_HOST")
	if host == "" {
		tb.Fatal("missing REDIS_HOST")
	}

	port := os.Getenv("REDIS_PORT")
	if port == "" {
		port = "6379"
	}

	pass := os.Getenv("REDIS_PASS")

	s, err := New(&Config{
		Tokens:   tokens,
		Interval: interval,
		Dial: func() (redis.Conn, error) {
			return redis.Dial("tcp", host+":"+port,
				redis.DialPassword(pass))
		},
	})
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() {
		if err := s.Close(context.Background()); err != nil {
			tb.Fatal(err)
		}
	})
	return s
}

// testTake takes from the key and fails the test on an error.
func testTake(tb testing.TB, s *Store, key string) (limit, remaining uint64, reset time.Time, ok bool) {
	tb.Helper()

	limit, remaining, next, ok, err := s.Take(context.Background(), key)
	if err != nil {
		tb.Fatal(err)
	}
	return limit, remaining, time.Unix(0, int64(next)), ok //nolint:gosec // nanosecond timestamp, fits until 2262
}

// testWaitForReset sleeps until the bucket's next interval has started.
func testWaitForReset(tb testing.TB, reset time.Time) {
	tb.Helper()

	time.Sleep(time.Until(reset) + 100*time.Millisecond)
}

// testHSet writes hash fields directly, to build the state that other releases
// of this package leave behind in Redis.
func testHSet(tb testing.TB, s *Store, key string, fieldValues ...interface{}) {
	tb.Helper()

	conn := s.pool.Get()
	defer func() {
		if err := conn.Close(); err != nil {
			tb.Fatal(err)
		}
	}()

	if _, err := conn.Do(cmdHSET, append([]interface{}{key}, fieldValues...)...); err != nil {
		tb.Fatal(err)
	}
	if _, err := conn.Do(cmdEXPIRE, key, 30); err != nil {
		tb.Fatal(err)
	}
}

func TestStore_Exercise(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	host := os.Getenv("REDIS_HOST")
	if host == "" {
		t.Fatal("missing REDIS_HOST")
	}

	port := os.Getenv("REDIS_PORT")
	if port == "" {
		port = "6379"
	}

	pass := os.Getenv("REDIS_PASS")

	s, err := New(&Config{
		Tokens:   5,
		Interval: 3 * time.Second,
		Dial: func() (redis.Conn, error) {
			return redis.Dial("tcp", host+":"+port,
				redis.DialPassword(pass))
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(ctx); err != nil {
			t.Fatal(err)
		}
	})

	key := testKey(t)

	// Get when no config exists
	{
		limit, remaining, err := s.Get(ctx, key)
		if err != nil {
			t.Fatal(err)
		}

		if got, want := limit, uint64(0); got != want {
			t.Errorf("expected %v to be %v", got, want)
		}
		if got, want := remaining, uint64(0); got != want {
			t.Errorf("expected %v to be %v", got, want)
		}
	}

	// Take with no key configuration - this should use the default values
	{
		limit, remaining, reset, ok, err := s.Take(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Errorf("expected ok")
		}
		if got, want := limit, uint64(5); got != want {
			t.Errorf("expected %v to be %v", got, want)
		}
		if got, want := remaining, uint64(4); got != want {
			t.Errorf("expected %v to be %v", got, want)
		}
		if got, want := time.Until(time.Unix(0, int64(reset))), 3*time.Second; got > want {
			t.Errorf("expected %v to less than %v", got, want)
		}
	}

	// Get the value
	{
		limit, remaining, err := s.Get(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := limit, uint64(5); got != want {
			t.Errorf("expected %v to be %v", got, want)
		}
		if got, want := remaining, uint64(4); got != want {
			t.Errorf("expected %v to be %v", got, want)
		}
	}

	// Now set a value
	{
		if err := s.Set(ctx, key, 11, 5*time.Second); err != nil {
			t.Fatal(err)
		}
	}

	// Get the value again
	{
		limit, remaining, err := s.Get(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := limit, uint64(11); got != want {
			t.Errorf("expected %v to be %v", got, want)
		}
		if got, want := remaining, uint64(11); got != want {
			t.Errorf("expected %v to be %v", got, want)
		}
	}

	// Take again, this should use the new values
	{
		limit, remaining, reset, ok, err := s.Take(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Errorf("expected ok")
		}
		if got, want := limit, uint64(11); got != want {
			t.Errorf("expected %v to be %v", got, want)
		}
		if got, want := remaining, uint64(10); got != want {
			t.Errorf("expected %v to be %v", got, want)
		}
		if got, want := time.Until(time.Unix(0, int64(reset))), 5*time.Second; got > want {
			t.Errorf("expected %v to less than %v", got, want)
		}
	}

	// Get the value again
	{
		limit, remaining, err := s.Get(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := limit, uint64(11); got != want {
			t.Errorf("expected %v to be %v", got, want)
		}
		if got, want := remaining, uint64(10); got != want {
			t.Errorf("expected %v to be %v", got, want)
		}
	}

	// Burst and take
	{
		if err := s.Burst(ctx, key, 5); err != nil {
			t.Fatal(err)
		}

		limit, remaining, reset, ok, err := s.Take(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Errorf("expected ok")
		}
		if got, want := limit, uint64(11); got != want {
			t.Errorf("expected %v to be %v", got, want)
		}
		if got, want := remaining, uint64(14); got != want {
			t.Errorf("expected %v to be %v", got, want)
		}
		if got, want := time.Until(time.Unix(0, int64(reset))), 5*time.Second; got > want {
			t.Errorf("expected %v to less than %v", got, want)
		}
	}

	// Get the value one final time
	{
		limit, remaining, err := s.Get(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := limit, uint64(11); got != want {
			t.Errorf("expected %v to be %v", got, want)
		}
		if got, want := remaining, uint64(14); got != want {
			t.Errorf("expected %v to be %v", got, want)
		}
	}
}

func TestStore_Take(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skipf("skipping (short)")
	}

	ctx := context.Background()

	host := os.Getenv("REDIS_HOST")
	if host == "" {
		t.Fatal("missing REDIS_HOST")
	}

	port := os.Getenv("REDIS_PORT")
	if port == "" {
		port = "6379"
	}

	pass := os.Getenv("REDIS_PASS")

	cases := []struct {
		name     string
		tokens   uint64
		interval time.Duration
	}{
		{
			name:     "second",
			tokens:   10,
			interval: 1 * time.Second,
		},
	}

	for _, tc := range cases {
		tc := tc

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			key := testKey(t)

			s, err := New(&Config{
				Interval: tc.interval,
				Tokens:   tc.tokens,
				Dial: func() (redis.Conn, error) {
					return redis.Dial("tcp", host+":"+port,
						redis.DialPassword(pass))
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := s.Close(ctx); err != nil {
					t.Fatal(err)
				}
			})

			type result struct {
				limit, remaining uint64
				reset            time.Duration
				ok               bool
				err              error
			}

			// Take twice everything
			takeCh := make(chan *result, 2*tc.tokens)
			for i := uint64(1); i <= 2*tc.tokens; i++ {
				go func() {
					limit, remaining, reset, ok, err := s.Take(ctx, key)
					takeCh <- &result{limit, remaining, time.Until(time.Unix(0, int64(reset))), ok, err}
				}()
			}

			// Accumulate and sort results, since they could come in any order
			var results []*result
			for i := uint64(1); i <= 2*tc.tokens; i++ {
				select {
				case result := <-takeCh:
					results = append(results, result)
				case <-time.After(5 * time.Second):
					t.Fatal("timeout")
				}
			}
			sort.Slice(results, func(i, j int) bool {
				if results[i].remaining == results[j].remaining {
					return !results[j].ok
				}
				return results[i].remaining > results[j].remaining
			})

			for i, result := range results {
				if err := result.err; err != nil {
					t.Fatal(err)
				}

				if got, want := result.limit, tc.tokens; got != want {
					t.Errorf("limit: expected %d to be %d", got, want)
				}
				if got, want := result.reset, tc.interval; got > want {
					t.Errorf("reset: expected %d to be less than %d", got, want)
				}

				// first half should pass, second half should fail
				if uint64(i) < tc.tokens {
					if got, want := result.remaining, tc.tokens-uint64(i)-1; got != want {
						t.Errorf("remaining: expected %d to be %d", got, want)
					}
					if got, want := result.ok, true; got != want {
						t.Errorf("ok: expected %t to be %t", got, want)
					}
				} else {
					if got, want := result.remaining, uint64(0); got != want {
						t.Errorf("remaining: expected %d to be %d", got, want)
					}
					if got, want := result.ok, false; got != want {
						t.Errorf("ok: expected %t to be %t", got, want)
					}
				}
			}

			// Wait for entries again
			time.Sleep(tc.interval)

			// Verify we can take once more
			_, _, _, ok, err := s.Take(ctx, key)
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				t.Errorf("expected %t to be %t", ok, true)
			}
		})
	}
}

func TestStore_Take_defaultsRaised(t *testing.T) {
	t.Parallel()

	key := testKey(t)
	before := testStore(t, 5, 2*time.Second)
	after := testStore(t, 9, 2*time.Second)

	limit, remaining, _, _ := testTake(t, before, key)
	if got, want := limit, uint64(5); got != want {
		t.Errorf("limit: expected %v to be %v", got, want)
	}
	if got, want := remaining, uint64(4); got != want {
		t.Errorf("remaining: expected %v to be %v", got, want)
	}

	// The key was never idle, so its hash still exists. The new limit applies,
	// but no tokens are added until the next interval.
	limit, remaining, reset, ok := testTake(t, after, key)
	if !ok {
		t.Errorf("expected ok")
	}
	if got, want := limit, uint64(9); got != want {
		t.Errorf("limit: expected %v to be %v", got, want)
	}
	if got, want := remaining, uint64(3); got != want {
		t.Errorf("remaining: expected %v to be %v", got, want)
	}

	testWaitForReset(t, reset)

	limit, remaining, _, _ = testTake(t, after, key)
	if got, want := limit, uint64(9); got != want {
		t.Errorf("limit: expected %v to be %v", got, want)
	}
	if got, want := remaining, uint64(8); got != want {
		t.Errorf("remaining: expected %v to be %v", got, want)
	}
}

func TestStore_Take_defaultsLowered(t *testing.T) {
	t.Parallel()

	key := testKey(t)
	before := testStore(t, 9, 2*time.Second)
	after := testStore(t, 3, 2*time.Second)

	if _, remaining, _, _ := testTake(t, before, key); remaining != 8 {
		t.Errorf("remaining: expected %v to be %v", remaining, 8)
	}

	// 8 tokens were left, the new limit caps them at 3.
	for _, want := range []uint64{2, 1, 0} {
		limit, remaining, _, ok := testTake(t, after, key)
		if !ok {
			t.Errorf("expected ok")
		}
		if got, want := limit, uint64(3); got != want {
			t.Errorf("limit: expected %v to be %v", got, want)
		}
		if got := remaining; got != want {
			t.Errorf("remaining: expected %v to be %v", got, want)
		}
	}

	if _, _, _, ok := testTake(t, after, key); ok {
		t.Errorf("expected not ok")
	}
}

func TestStore_Take_defaultIntervalChanged(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skipf("skipping (short)")
	}

	key := testKey(t)
	before := testStore(t, 3, 1*time.Second)
	after := testStore(t, 3, 3*time.Second)

	// Let the bucket tick twice with the old interval. A tick count that was
	// carried over to the longer interval would be ahead of the clock, and the
	// bucket would not refill until the clock caught up.
	for i := 0; i < 2; i++ {
		_, _, reset, _ := testTake(t, before, key)
		testWaitForReset(t, reset)
	}
	if _, remaining, _, _ := testTake(t, before, key); remaining != 2 {
		t.Errorf("remaining: expected %v to be %v", remaining, 2)
	}

	_, remaining, reset, ok := testTake(t, after, key)
	if !ok {
		t.Errorf("expected ok")
	}
	if got, want := remaining, uint64(1); got != want {
		t.Errorf("remaining: expected %v to be %v", got, want)
	}
	if got, lo, hi := time.Until(reset), 2*time.Second, 3*time.Second; got < lo || got > hi {
		t.Errorf("reset: expected %v to be between %v and %v", got, lo, hi)
	}

	if _, _, _, ok := testTake(t, after, key); !ok {
		t.Errorf("expected ok")
	}

	// The old interval has passed, the new one has not.
	time.Sleep(1200 * time.Millisecond)
	if _, _, _, ok := testTake(t, after, key); ok {
		t.Errorf("expected not ok")
	}

	testWaitForReset(t, reset)

	_, remaining, _, ok = testTake(t, after, key)
	if !ok {
		t.Errorf("expected ok")
	}
	if got, want := remaining, uint64(2); got != want {
		t.Errorf("remaining: expected %v to be %v", got, want)
	}
}

// Two stores with different defaults share a key for as long as a rolling
// deploy runs, and the bucket follows whichever store took last.
func TestStore_Take_defaultsAlternate(t *testing.T) {
	t.Parallel()

	key := testKey(t)
	before := testStore(t, 10, 2*time.Second)
	after := testStore(t, 4, 2*time.Second)

	_, _, reset, _ := testTake(t, before, key)
	admitted := 1
	for i := 0; i < 20; i++ {
		for _, s := range []*Store{after, before} {
			if _, _, _, ok := testTake(t, s, key); ok {
				admitted++
			}
		}
	}
	if lo, hi := 4, 10; admitted < lo || admitted > hi {
		t.Errorf("admitted: expected %v to be between %v and %v", admitted, lo, hi)
	}

	testWaitForReset(t, reset)

	// The deploy is over, only the new store is left.
	admitted = 0
	for i := 0; i < 6; i++ {
		limit, _, _, ok := testTake(t, after, key)
		if ok {
			admitted++
		}
		if got, want := limit, uint64(4); got != want {
			t.Errorf("limit: expected %v to be %v", got, want)
		}
	}
	if got, want := admitted, 4; got != want {
		t.Errorf("admitted: expected %v to be %v", got, want)
	}
}

func TestStore_Take_setOutlivesDefaults(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	cases := []struct {
		name string

		// takeFirst creates the bucket from the defaults before Set is called.
		takeFirst bool
		tokens    uint64
		interval  time.Duration
		remaining uint64
	}{
		{
			name:      "set_on_new_key",
			tokens:    11,
			interval:  5 * time.Second,
			remaining: 10,
		},
		{
			name:      "set_on_existing_key",
			takeFirst: true,
			tokens:    11,
			interval:  5 * time.Second,
			remaining: 10,
		},
		{
			name:      "set_to_the_defaults",
			takeFirst: true,
			tokens:    5,
			interval:  2 * time.Second,
			remaining: 4,
		},
	}

	for _, tc := range cases {
		tc := tc

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			key := testKey(t)
			before := testStore(t, 5, 2*time.Second)
			after := testStore(t, 9, 2*time.Second)

			if tc.takeFirst {
				testTake(t, before, key)
			}
			if err := before.Set(ctx, key, tc.tokens, tc.interval); err != nil {
				t.Fatal(err)
			}

			limit, remaining, _, _ := testTake(t, after, key)
			if got, want := limit, tc.tokens; got != want {
				t.Errorf("limit: expected %v to be %v", got, want)
			}
			if got, want := remaining, tc.remaining; got != want {
				t.Errorf("remaining: expected %v to be %v", got, want)
			}
		})
	}
}

// Hashes written by releases that did not record where a limit came from.
func TestStore_Take_untrackedBucket(t *testing.T) {
	t.Parallel()

	now := time.Now().UnixNano()

	cases := []struct {
		name string

		// takeFirst creates the bucket from the defaults before fields is written.
		takeFirst bool
		fields    []interface{}
		limit     uint64
	}{
		{
			// Indistinguishable from a bucket that Take created, so it follows the
			// defaults from here on.
			name:   "matches_defaults",
			fields: []interface{}{"s", now, "t", 0, "m", 5, "k", 5},
			limit:  9,
		},
		{
			// Could be a Set, so it is left alone.
			name:   "differs_from_defaults",
			fields: []interface{}{"s", now, "t", 0, "m", 7, "k", 7},
			limit:  7,
		},
		{
			// Set from an older release, which does not mark the bucket.
			name:      "old_set_on_tracked_bucket",
			takeFirst: true,
			fields:    []interface{}{"k", 11, "m", 11, "i", (5 * time.Second).Nanoseconds()},
			limit:     11,
		},
	}

	for _, tc := range cases {
		tc := tc

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			key := testKey(t)
			before := testStore(t, 5, 2*time.Second)
			after := testStore(t, 9, 2*time.Second)

			if tc.takeFirst {
				testTake(t, before, key)
			}
			testHSet(t, before, key, tc.fields...)
			testTake(t, before, key)

			limit, _, _, _ := testTake(t, after, key)
			if got, want := limit, tc.limit; got != want {
				t.Errorf("limit: expected %v to be %v", got, want)
			}
		})
	}
}
