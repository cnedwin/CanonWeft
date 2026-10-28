package memory

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestCacheHasTTLByteBoundAndCopyIsolation(t *testing.T) {
	now := time.Unix(100, 0)
	c := NewLocalCache(2, 160)
	c.now = func() time.Time { return now }
	a, b, d := "memory:"+digest("a"), "memory:"+digest("b"), "memory:"+digest("d")
	value := []byte("hello")
	if err := c.Put(context.Background(), a, value, time.Minute); err != nil {
		t.Fatal(err)
	}
	value[0] = 'X'
	got, ok, err := c.Get(context.Background(), a)
	if err != nil || !ok || string(got) != "hello" {
		t.Fatal(string(got), ok, err)
	}
	got[0] = 'Y'
	got, _, _ = c.Get(context.Background(), a)
	if string(got) != "hello" {
		t.Fatal("cache mutated through returned slice")
	}
	c.Put(context.Background(), b, []byte("world"), time.Minute)
	c.Get(context.Background(), a)
	c.Put(context.Background(), d, []byte("other"), time.Minute)
	if _, ok, _ := c.Get(context.Background(), b); ok {
		t.Fatal("LRU limit not enforced")
	}
	if c.Bytes() > 160 {
		t.Fatal("byte capacity exceeded")
	}
	now = now.Add(time.Minute)
	if _, ok, _ := c.Get(context.Background(), a); ok {
		t.Fatal("expired memory survived TTL")
	}
}
func TestCacheRejectsOversizeWithoutEvictingAndHandlesConcurrency(t *testing.T) {
	c := NewLocalCache(8, 2048)
	ctx := context.Background()
	key := "memory:" + digest("large")
	if err := c.Put(ctx, key, make([]byte, 4096), time.Minute); err == nil {
		t.Fatal("oversize accepted")
	}
	var group sync.WaitGroup
	for i := 0; i < 16; i++ {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			for n := 0; n < 100; n++ {
				key := "memory:" + digest(i)
				c.Put(ctx, key, []byte("value"), time.Second)
				c.Get(ctx, key)
			}
		}(i)
	}
	group.Wait()
	if c.Bytes() > 2048 {
		t.Fatal("concurrent capacity exceeded")
	}
	c.Clear()
	if c.Bytes() != 0 {
		t.Fatal("clear retained data")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := c.Get(cancelled, key); err == nil {
		t.Fatal("cancel ignored")
	}
}
