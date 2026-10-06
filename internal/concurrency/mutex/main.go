// This example demonstrates sync.Mutex and sync.RWMutex.
//
// In Java the analogs are ReentrantLock and ReentrantReadWriteLock.
// Key differences to internalize:
//   - Go's Mutex is NOT reentrant: Lock() twice from one goroutine deadlocks.
//     Java's synchronized/ReentrantLock allows reentry.
//   - Go's RWMutex gives priority to waiting writers: once a writer waits,
//     new readers block. Java's default ReentrantReadWriteLock lets readers
//     barge, which can starve writers.
//
// Run: go run -race ./internal/concurrency/mutex
package main

import (
	"fmt"
	"sync"
)

// SafeCounter guards a map with a Mutex.
// In Java: class guarded by `synchronized` methods or a ReentrantLock field.
type SafeCounter struct {
	mu sync.Mutex
	m  map[string]int
}

func NewSafeCounter() *SafeCounter { return &SafeCounter{m: make(map[string]int)} }

// Inc holds the lock for the whole read-modify-write.
// Idiomatic Go: lock, then `defer Unlock()` immediately so every return
// path releases it. In Java: lock.lock(); try { ... } finally { unlock(); }
func (c *SafeCounter) Inc(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[key]++
}

func (c *SafeCounter) Get(key string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m[key]
}

// Cache shows when RWMutex pays off: many readers, few writers.
// Readers hold RLock concurrently; a writer excludes everyone.
type Cache struct {
	mu   sync.RWMutex
	data map[string]int
}

func NewCache() *Cache { return &Cache{data: make(map[string]int)} }

func (c *Cache) Get(key string) (int, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.data[key]
	return v, ok
}

func (c *Cache) Set(key string, v int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data[key] = v
}

func main() {
	fmt.Println("== Mutex: 100 goroutines x 1000 increments ==")
	counter := NewSafeCounter()
	var wg sync.WaitGroup
	for g := 0; g < 100; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				counter.Inc("hits")
			}
		}()
	}
	wg.Wait()
	fmt.Println("hits =", counter.Get("hits"), "(expect 100000)")

	fmt.Println("== RWMutex: concurrent readers + one writer ==")
	cache := NewCache()
	cache.Set("k", 42)
	for g := 0; g < 5; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			v, _ := cache.Get("k")
			fmt.Printf("reader %d saw %d\n", id, v)
		}(g)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Why writers win: RWMutex blocks NEW readers once Lock() is waiting,
		// so this write cannot be starved by a stream of readers.
		cache.Set("k", 43)
	}()
	wg.Wait()
	v, _ := cache.Get("k")
	fmt.Println("final k =", v)

	// NOTE (non-reentrant): uncommenting the next two lines deadlocks.
	// var mu sync.Mutex; mu.Lock(); mu.Lock() // fatal: all goroutines asleep
	fmt.Println("NOTE: sync.Mutex is not reentrant — a second Lock() deadlocks.")
}
