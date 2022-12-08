package redisstore_test

import (
	"context"
	"log"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/sethvargo/go-redisstore"
)

func ExampleNew() {
	ctx := context.Background()

	store, err := redisstore.New(&redisstore.Config{
		Tokens:   15,
		Interval: time.Minute,
		RedisOptions: &redis.Options{
			Addr: "127.0.0.1:6379",
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close(ctx)

	limit, remaining, reset, ok, err := store.Take(ctx, "my-key")
	if err != nil {
		log.Fatal(err)
	}
	_, _, _, _ = limit, remaining, reset, ok
}
