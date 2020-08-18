package redisstore_test

import (
	"log"
	"time"

	"github.com/gomodule/redigo/redis"
	"github.com/sethvargo/go-redisstore"
)

func ExampleNew() {
	store, err := redisstore.New(&redisstore.Config{
		Tokens:   15,
		Interval: time.Minute,
		Dial: func() (redis.Conn, error) {
			return redis.Dial("tcp", "127.0.0,1:6379",
				redis.DialPassword("my-password"))
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()

	limit, remaining, reset, ok, err := store.Take("my-key")
	if err != nil {
		log.Fatal(err)
	}
	_, _, _, _ = limit, remaining, reset, ok
}
