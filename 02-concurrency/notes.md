# Chapter 02 — Concurrency

Go's concurrency model is built into the language, not a library. It's based on CSP (Communicating Sequential Processes) — the idea that goroutines communicate by passing messages through channels rather than sharing memory through locks (though both are supported).

> "Do not communicate by sharing memory; instead, share memory by communicating." — Go proverb

---

## 1. Goroutines

A goroutine is a lightweight thread managed by the Go runtime. Starting one is cheap — roughly 2–4 KB of stack (vs ~8 MB for an OS thread). The runtime grows stacks dynamically as needed.

```go
package main

import (
    "fmt"
    "time"
)

func sayHello(name string) {
    fmt.Println("Hello,", name)
}

func main() {
    go sayHello("Alice")   // start goroutine
    go sayHello("Bob")

    // main goroutine must not exit before the others finish
    // (bad solution — see WaitGroup below)
    time.Sleep(100 * time.Millisecond)
}
```

### Goroutines vs OS Threads (M:N Scheduling)

| | Goroutine | OS Thread |
|--|-----------|-----------|
| Initial stack | ~2–4 KB | ~2–8 MB |
| Stack growth | Dynamic | Fixed |
| Scheduling | Go runtime (user-space) | OS kernel |
| Context switch | ~100ns | ~1–10µs |
| Practical limit | Hundreds of thousands | Thousands |

The Go scheduler uses M:N scheduling: M goroutines run on N OS threads (where N = `GOMAXPROCS`, defaults to number of CPUs). The scheduler is work-stealing — idle threads steal goroutines from busy ones.

### Goroutine Leaks

A goroutine that is started but never exits is a **goroutine leak**. Leaked goroutines accumulate, consuming memory and potentially holding resources (file handles, DB connections).

```go
// LEAK: goroutine blocks forever because nobody reads from ch
func leaky() {
    ch := make(chan int)
    go func() {
        result := compute()
        ch <- result  // blocks forever if no one reads
    }()
    // function returns without reading ch
    // goroutine is stuck forever
}

// FIX: use a buffered channel, or a context for cancellation
func notLeaky(ctx context.Context) {
    ch := make(chan int, 1) // buffered — send won't block
    go func() {
        select {
        case ch <- compute():
        case <-ctx.Done(): // respect cancellation
        }
    }()
}
```

Use `goleak` in tests to detect goroutine leaks:

```go
import "go.uber.org/goleak"

func TestMain(m *testing.M) {
    goleak.VerifyTestMain(m)
}
```

---

## 2. Channels

Channels are typed conduits for communication between goroutines. The type system ensures type-safe message passing.

```go
ch := make(chan int)      // unbuffered channel
ch := make(chan int, 10)  // buffered channel, capacity 10
ch := make(chan string)
ch := make(chan []byte)
ch := make(chan struct{}) // zero-size signal channel (common pattern)
```

### Unbuffered (Synchronous)

An unbuffered channel synchronizes sender and receiver. The send blocks until a receiver is ready. The receive blocks until a sender sends.

```go
func main() {
    ch := make(chan string)

    go func() {
        // This goroutine sends, then blocks until main receives
        ch <- "hello from goroutine"
    }()

    msg := <-ch // blocks until the goroutine sends
    fmt.Println(msg) // hello from goroutine
}
```

### Buffered (Asynchronous up to capacity)

A buffered channel holds values without a receiver present — up to its capacity.

```go
ch := make(chan int, 3)
ch <- 1  // doesn't block
ch <- 2  // doesn't block
ch <- 3  // doesn't block
ch <- 4  // BLOCKS — buffer full

fmt.Println(<-ch) // 1
fmt.Println(<-ch) // 2
```

### Closing Channels

Closing signals "no more values will be sent." Receiving from a closed channel returns the zero value immediately:

```go
ch := make(chan int, 3)
ch <- 1
ch <- 2
ch <- 3
close(ch)

for v := range ch {
    fmt.Println(v) // prints 1, 2, 3, then loop exits
}

// Manual check for closed:
v, ok := <-ch
if !ok {
    fmt.Println("channel closed")
}
```

**Rules:**
- Only the sender should close a channel
- Never close a channel from the receiver side
- Closing a nil channel panics
- Closing an already-closed channel panics
- Sending to a closed channel panics

### Directional Channels

Restrict a channel to send-only or receive-only in function signatures:

```go
// chan<- string: send-only
func producer(ch chan<- string) {
    ch <- "message"
    // <-ch  // compile error: cannot receive from send-only channel
}

// <-chan string: receive-only
func consumer(ch <-chan string) {
    msg := <-ch
    fmt.Println(msg)
    // ch <- "reply"  // compile error: cannot send to receive-only channel
}

func main() {
    ch := make(chan string, 1)
    go producer(ch) // bidirectional chan converts to chan<-
    consumer(ch)    // bidirectional chan converts to <-chan
}
```

---

## 3. Select

`select` is like a `switch` for channel operations. It waits until one of its cases can proceed, then executes that case. If multiple cases are ready simultaneously, one is chosen at random.

```go
func main() {
    ch1 := make(chan string)
    ch2 := make(chan string)

    go func() { time.Sleep(1 * time.Second); ch1 <- "one" }()
    go func() { time.Sleep(2 * time.Second); ch2 <- "two" }()

    for i := 0; i < 2; i++ {
        select {
        case msg := <-ch1:
            fmt.Println("received from ch1:", msg)
        case msg := <-ch2:
            fmt.Println("received from ch2:", msg)
        }
    }
}
```

### Default Case (Non-blocking)

```go
ch := make(chan int, 1)

// Non-blocking send:
select {
case ch <- 42:
    fmt.Println("sent")
default:
    fmt.Println("channel full, dropped")
}

// Non-blocking receive:
select {
case v := <-ch:
    fmt.Println("got:", v)
default:
    fmt.Println("nothing to receive")
}
```

### Timeout Pattern

```go
func fetchWithTimeout(url string, timeout time.Duration) (string, error) {
    resultCh := make(chan string, 1)
    errCh := make(chan error, 1)

    go func() {
        resp, err := http.Get(url)
        if err != nil {
            errCh <- err
            return
        }
        defer resp.Body.Close()
        body, err := io.ReadAll(resp.Body)
        if err != nil {
            errCh <- err
            return
        }
        resultCh <- string(body)
    }()

    select {
    case result := <-resultCh:
        return result, nil
    case err := <-errCh:
        return "", err
    case <-time.After(timeout):
        return "", fmt.Errorf("request timed out after %s", timeout)
    }
}
```

---

## 4. sync Package

### WaitGroup — Wait for Multiple Goroutines

```go
func main() {
    var wg sync.WaitGroup

    for i := 0; i < 5; i++ {
        wg.Add(1)        // increment counter
        go func(id int) {
            defer wg.Done() // decrement counter when goroutine exits
            fmt.Printf("worker %d done\n", id)
        }(i)
    }

    wg.Wait() // block until counter reaches 0
    fmt.Println("all workers done")
}
```

### Mutex — Mutual Exclusion

```go
type SafeCounter struct {
    mu    sync.Mutex
    count int
}

func (c *SafeCounter) Increment() {
    c.mu.Lock()
    defer c.mu.Unlock()
    c.count++
}

func (c *SafeCounter) Value() int {
    c.mu.Lock()
    defer c.mu.Unlock()
    return c.count
}

func main() {
    c := &SafeCounter{}
    var wg sync.WaitGroup

    for i := 0; i < 1000; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            c.Increment()
        }()
    }
    wg.Wait()
    fmt.Println(c.Value()) // 1000
}
```

### RWMutex — Concurrent Reads, Exclusive Writes

```go
type Cache struct {
    mu    sync.RWMutex
    items map[string]string
}

func (c *Cache) Get(key string) (string, bool) {
    c.mu.RLock()          // multiple readers can hold RLock simultaneously
    defer c.mu.RUnlock()
    v, ok := c.items[key]
    return v, ok
}

func (c *Cache) Set(key, value string) {
    c.mu.Lock()           // exclusive lock — no readers or writers
    defer c.mu.Unlock()
    c.items[key] = value
}
```

### sync.Once — Run Exactly Once

```go
var (
    instance *Database
    once     sync.Once
)

func GetDB() *Database {
    once.Do(func() {
        instance = &Database{
            conn: openConnection(),
        }
    })
    return instance
}
```

### sync.Map — Concurrent Map

Use when you have many goroutines reading/writing different keys. For a fixed set of keys written once, a regular map with RWMutex is faster.

```go
var m sync.Map

// Store
m.Store("key", "value")

// Load
v, ok := m.Load("key")
if ok {
    fmt.Println(v.(string))
}

// LoadOrStore — atomic check-and-set
actual, loaded := m.LoadOrStore("key", "default")
// loaded=true means key existed, actual is the existing value
// loaded=false means key was stored, actual is the new value

// Delete
m.Delete("key")

// Range — iterate (no guaranteed order)
m.Range(func(key, value any) bool {
    fmt.Printf("%v: %v\n", key, value)
    return true // return false to stop iteration
})
```

### When to Use Channels vs Mutexes

| Use channels when | Use mutexes when |
|-------------------|-----------------|
| Passing ownership of data | Protecting shared state (cache, counter) |
| Coordinating goroutines | Simple read/write protection |
| Pipelines and workflows | Structs with multiple fields to protect atomically |
| One-to-one goroutine communication | High-frequency, low-contention reads |

---

## 5. sync/atomic

For simple integer counters and boolean flags, atomic operations are faster than mutexes.

```go
import "sync/atomic"

var (
    requestCount int64
    isShutdown   atomic.Bool  // Go 1.19+ typed atomics
)

// Old style (still works):
atomic.AddInt64(&requestCount, 1)
count := atomic.LoadInt64(&requestCount)

// New style (Go 1.19+):
var counter atomic.Int64
counter.Add(1)
counter.Store(42)
val := counter.Load()
swapped := counter.CompareAndSwap(42, 0) // CAS operation
```

---

## 6. context Package

The `context` package propagates cancellation, deadlines, and request-scoped values through a call tree. Pass `ctx` as the first argument to every function that does I/O or calls other goroutines.

```go
import "context"
```

### WithCancel

```go
ctx, cancel := context.WithCancel(context.Background())
defer cancel() // ALWAYS defer cancel to prevent context leak

go func(ctx context.Context) {
    for {
        select {
        case <-ctx.Done():
            fmt.Println("goroutine stopping:", ctx.Err())
            return
        default:
            doWork()
        }
    }
}(ctx)

time.Sleep(2 * time.Second)
cancel() // signal the goroutine to stop
```

### WithTimeout and WithDeadline

```go
// Timeout: cancel after a duration
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()

// Deadline: cancel at a specific time
deadline := time.Now().Add(5 * time.Second)
ctx, cancel := context.WithDeadline(context.Background(), deadline)
defer cancel()

// Check if context is done:
select {
case <-ctx.Done():
    return ctx.Err() // context.DeadlineExceeded or context.Canceled
default:
    // still running
}

// Most stdlib functions accept ctx and handle it:
req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
resp, err := http.DefaultClient.Do(req)
if err != nil {
    // err might be context.DeadlineExceeded
}
```

### WithValue

Pass request-scoped values (trace IDs, user IDs, auth tokens) down the call stack without polluting function signatures.

```go
type contextKey string

const (
    requestIDKey contextKey = "requestID"
    userIDKey    contextKey = "userID"
)

func middleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        reqID := generateRequestID()
        ctx := context.WithValue(r.Context(), requestIDKey, reqID)
        next.ServeHTTP(w, r.WithContext(ctx))
    })
}

func handler(w http.ResponseWriter, r *http.Request) {
    reqID, ok := r.Context().Value(requestIDKey).(string)
    if ok {
        fmt.Println("request ID:", reqID)
    }
}
```

**Best practices:**
- Use unexported custom key types (like `contextKey` above) to avoid collisions
- Only pass request-scoped values, not optional function parameters
- Context values are not type-safe — always use a type assertion with the `ok` check
- Don't store contexts in structs — pass them as the first function argument

---

## 7. Concurrency Patterns

### Fan-Out / Fan-In

Fan-out: distribute work across multiple goroutines.  
Fan-in: merge results from multiple goroutines into one channel.

```go
package main

import (
    "fmt"
    "sync"
)

// fanOut sends each item from input to one of numWorkers goroutines
func fanOut(input <-chan int, numWorkers int) []<-chan int {
    channels := make([]<-chan int, numWorkers)
    for i := 0; i < numWorkers; i++ {
        ch := make(chan int)
        channels[i] = ch
        go func(out chan<- int) {
            for v := range input {
                out <- v * v // square the number
            }
            close(out)
        }(ch)
    }
    return channels
}

// fanIn merges multiple channels into one
func fanIn(channels ...<-chan int) <-chan int {
    merged := make(chan int)
    var wg sync.WaitGroup

    output := func(ch <-chan int) {
        defer wg.Done()
        for v := range ch {
            merged <- v
        }
    }

    wg.Add(len(channels))
    for _, ch := range channels {
        go output(ch)
    }

    go func() {
        wg.Wait()
        close(merged)
    }()

    return merged
}

func main() {
    input := make(chan int, 10)
    for i := 1; i <= 10; i++ {
        input <- i
    }
    close(input)

    workers := fanOut(input, 3)
    results := fanIn(workers...)

    for v := range results {
        fmt.Println(v)
    }
}
```

### Pipeline

Chain processing stages, each running concurrently:

```go
// Stage 1: generate numbers
func generate(nums ...int) <-chan int {
    out := make(chan int)
    go func() {
        defer close(out)
        for _, n := range nums {
            out <- n
        }
    }()
    return out
}

// Stage 2: square each number
func square(in <-chan int) <-chan int {
    out := make(chan int)
    go func() {
        defer close(out)
        for n := range in {
            out <- n * n
        }
    }()
    return out
}

// Stage 3: filter even numbers
func filterEven(in <-chan int) <-chan int {
    out := make(chan int)
    go func() {
        defer close(out)
        for n := range in {
            if n%2 == 0 {
                out <- n
            }
        }
    }()
    return out
}

func main() {
    // Connect the pipeline
    nums := generate(1, 2, 3, 4, 5, 6, 7, 8, 9, 10)
    squares := square(nums)
    evens := filterEven(squares)

    for v := range evens {
        fmt.Println(v) // 4, 16, 36, 64, 100
    }
}
```

### Worker Pool

Fixed number of goroutines processing a job queue:

```go
package main

import (
    "fmt"
    "sync"
    "time"
)

type Job struct {
    ID int
}

type Result struct {
    JobID  int
    Output int
}

func worker(id int, jobs <-chan Job, results chan<- Result, wg *sync.WaitGroup) {
    defer wg.Done()
    for job := range jobs {
        // simulate work
        time.Sleep(10 * time.Millisecond)
        results <- Result{
            JobID:  job.ID,
            Output: job.ID * job.ID,
        }
        fmt.Printf("worker %d processed job %d\n", id, job.ID)
    }
}

func main() {
    const numWorkers = 3
    const numJobs = 10

    jobs := make(chan Job, numJobs)
    results := make(chan Result, numJobs)

    var wg sync.WaitGroup

    // Start workers
    for i := 1; i <= numWorkers; i++ {
        wg.Add(1)
        go worker(i, jobs, results, &wg)
    }

    // Send jobs
    for j := 1; j <= numJobs; j++ {
        jobs <- Job{ID: j}
    }
    close(jobs) // signal workers that no more jobs are coming

    // Wait for all workers to finish, then close results
    go func() {
        wg.Wait()
        close(results)
    }()

    // Collect results
    for r := range results {
        fmt.Printf("result: job %d -> %d\n", r.JobID, r.Output)
    }
}
```

### Rate Limiter (Token Bucket)

```go
package main

import (
    "fmt"
    "time"
)

func main() {
    requests := make(chan int, 10)
    for i := 1; i <= 10; i++ {
        requests <- i
    }
    close(requests)

    // Allow 2 requests per second
    limiter := time.NewTicker(500 * time.Millisecond)
    defer limiter.Stop()

    for req := range requests {
        <-limiter.C // block until a tick is available
        fmt.Printf("request %d at %s\n", req, time.Now().Format("15:04:05.000"))
    }
}

// Burst-capable token bucket:
func newRateLimiter(rate int, burst int) chan struct{} {
    tokens := make(chan struct{}, burst)

    // Fill with initial burst
    for i := 0; i < burst; i++ {
        tokens <- struct{}{}
    }

    // Refill at rate per second
    ticker := time.NewTicker(time.Second / time.Duration(rate))
    go func() {
        for range ticker.C {
            select {
            case tokens <- struct{}{}:
            default: // bucket full, discard
            }
        }
    }()

    return tokens
}
```

### Semaphore

Limit the number of concurrent operations:

```go
package main

import (
    "fmt"
    "sync"
    "time"
)

// Semaphore using a buffered channel
type Semaphore struct {
    ch chan struct{}
}

func NewSemaphore(n int) *Semaphore {
    return &Semaphore{ch: make(chan struct{}, n)}
}

func (s *Semaphore) Acquire() {
    s.ch <- struct{}{} // blocks if n slots are taken
}

func (s *Semaphore) Release() {
    <-s.ch
}

func main() {
    // Allow max 3 concurrent goroutines
    sem := NewSemaphore(3)
    var wg sync.WaitGroup

    for i := 1; i <= 10; i++ {
        wg.Add(1)
        go func(id int) {
            defer wg.Done()
            sem.Acquire()
            defer sem.Release()

            fmt.Printf("goroutine %d running\n", id)
            time.Sleep(100 * time.Millisecond)
            fmt.Printf("goroutine %d done\n", id)
        }(i)
    }

    wg.Wait()
}

// Or use golang.org/x/sync/semaphore (production-ready, supports context):
// sem := semaphore.NewWeighted(3)
// sem.Acquire(ctx, 1)
// sem.Release(1)
```

---

## 8. Common Mistakes

### Goroutine Leak

```go
// BAD: goroutine blocked forever, nobody reads ch
func bad() {
    ch := make(chan int)
    go func() {
        ch <- expensiveComputation()
    }()
    // function returns, nobody reads ch, goroutine is stuck
}

// GOOD: buffered channel or context cancellation
func good(ctx context.Context) (int, error) {
    ch := make(chan int, 1)
    errCh := make(chan error, 1)
    go func() {
        result, err := expensiveComputation()
        if err != nil {
            errCh <- err
            return
        }
        select {
        case ch <- result:
        case <-ctx.Done(): // exit if caller cancelled
        }
    }()
    select {
    case result := <-ch:
        return result, nil
    case err := <-errCh:
        return 0, err
    case <-ctx.Done():
        return 0, ctx.Err()
    }
}
```

### Channel Deadlock

```go
// DEADLOCK: main goroutine sends then immediately tries to receive
// but there's nobody else to receive the send
func deadlock() {
    ch := make(chan int)
    ch <- 1      // blocks forever: unbuffered, no receiver
    fmt.Println(<-ch)
}

// FIX 1: buffered channel
func fix1() {
    ch := make(chan int, 1)
    ch <- 1
    fmt.Println(<-ch)
}

// FIX 2: goroutine for the send
func fix2() {
    ch := make(chan int)
    go func() { ch <- 1 }()
    fmt.Println(<-ch)
}
```

### Race Conditions — Use -race

```bash
go run -race main.go
go test -race ./...
```

```go
// RACE: two goroutines read and write shared variable without synchronization
var counter int

func raceCondition() {
    var wg sync.WaitGroup
    for i := 0; i < 100; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            counter++ // DATA RACE: read-modify-write without lock
        }()
    }
    wg.Wait()
    fmt.Println(counter) // undefined result: could be anything from 1 to 100
}

// FIX: use atomic or mutex
var atomicCounter atomic.Int64

func noRace() {
    var wg sync.WaitGroup
    for i := 0; i < 100; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            atomicCounter.Add(1) // safe
        }()
    }
    wg.Wait()
    fmt.Println(atomicCounter.Load()) // 100
}
```

### Closing a Closed Channel

```go
// PANIC: close of closed channel
ch := make(chan int)
close(ch)
close(ch) // panic!

// PANIC: send on closed channel
ch <- 1 // panic!

// Pattern: use once to close exactly once
type SafeChan struct {
    ch     chan struct{}
    once   sync.Once
}

func (sc *SafeChan) Close() {
    sc.once.Do(func() {
        close(sc.ch)
    })
}
```

### The "for range over channel" Gotcha

```go
ch := make(chan int, 3)
ch <- 1
ch <- 2
ch <- 3
// forgot to close(ch)

for v := range ch {  // BLOCKS FOREVER after receiving 1, 2, 3
    fmt.Println(v)   // because range waits for the channel to be closed
}

// FIX: always close the channel when you're done sending
close(ch)
for v := range ch { // exits after 1, 2, 3
    fmt.Println(v)
}
```

---

## Quick Reference: Concurrency Primitives

```go
// Goroutine
go func() { /* ... */ }()

// Unbuffered channel
ch := make(chan T)

// Buffered channel
ch := make(chan T, capacity)

// Send (blocks if unbuffered and no receiver, or buffered and full)
ch <- value

// Receive (blocks until value available)
value := <-ch
value, ok := <-ch  // ok=false when channel closed and empty

// Close
close(ch)  // sender closes; signals no more values

// Range over channel (exits when channel closed and empty)
for v := range ch { ... }

// Select
select {
case v := <-ch1:    // receive
case ch2 <- v:      // send
case <-time.After(d): // timeout
default:            // non-blocking
}

// WaitGroup
var wg sync.WaitGroup
wg.Add(n)
go func() { defer wg.Done(); /* ... */ }()
wg.Wait()

// Mutex
var mu sync.Mutex
mu.Lock()
defer mu.Unlock()

// RWMutex
var rw sync.RWMutex
rw.RLock(); defer rw.RUnlock()  // readers
rw.Lock();  defer rw.Unlock()   // writer

// Once
var once sync.Once
once.Do(func() { /* run exactly once */ })

// Atomic
var n atomic.Int64
n.Add(1); n.Load(); n.Store(0); n.CompareAndSwap(old, new)
```
