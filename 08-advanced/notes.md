# Chapter 8: Advanced Go — Production-Level Patterns

## Overview

This chapter covers the Go features and patterns that separate beginner code from production code: generics, testing discipline, performance profiling, build tooling, architectural patterns, and the anti-patterns you must actively avoid.

---

## Section 1: Generics (Go 1.18+)

Generics allow you to write functions and types that work with multiple types without code duplication or empty interface gymnastics.

### 1.1 Basic Syntax

```go
// A generic function has a type parameter list in square brackets.
// [T any] means T can be any type.
func Map[T, U any](slice []T, fn func(T) U) []U {
    result := make([]U, len(slice))
    for i, v := range slice {
        result[i] = fn(v)
    }
    return result
}

// Usage — Go infers the type parameters from the arguments:
nums := []int{1, 2, 3, 4, 5}
doubled := Map(nums, func(n int) int { return n * 2 })
// doubled = [2, 4, 6, 8, 10]

strs := Map(nums, func(n int) string { return fmt.Sprintf("item-%d", n) })
// strs = ["item-1", "item-2", ...]
```

### 1.2 Constraints

Constraints restrict which types are accepted. Without constraints, you can only do operations valid for all types (assignment, comparison to nil, passing to interface methods).

```go
// "comparable" is a built-in constraint: types that support == and !=
func Contains[T comparable](slice []T, target T) bool {
    for _, v := range slice {
        if v == target {
            return true
        }
    }
    return false
}

// Union type constraint: only int, int64, float64 are allowed
type Number interface {
    int | int64 | float32 | float64
}

func Sum[T Number](nums []T) T {
    var total T
    for _, n := range nums {
        total += n
    }
    return total
}

// "~int" means: int AND any type whose underlying type is int
type Celsius float64
type Fahrenheit float64

type Temperature interface {
    ~float64 | ~float32
}

func Average[T Temperature](temps []T) T {
    if len(temps) == 0 {
        return 0
    }
    var sum T
    for _, t := range temps {
        sum += t
    }
    return sum / T(len(temps))
}

// Ordered: types that support < > <= >=
// This is defined in golang.org/x/exp/constraints but you can define it yourself:
type Ordered interface {
    ~int | ~int8 | ~int16 | ~int32 | ~int64 |
        ~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 |
        ~float32 | ~float64 | ~string
}

func Min[T Ordered](a, b T) T {
    if a < b {
        return a
    }
    return b
}

func Max[T Ordered](a, b T) T {
    if a > b {
        return a
    }
    return b
}

func Clamp[T Ordered](val, lo, hi T) T {
    return Max(lo, Min(val, hi))
}
```

### 1.3 Filter, Reduce

```go
// Filter keeps elements where predicate returns true.
func Filter[T any](slice []T, predicate func(T) bool) []T {
    var result []T
    for _, v := range slice {
        if predicate(v) {
            result = append(result, v)
        }
    }
    return result
}

// Reduce folds a slice into a single value.
func Reduce[T, U any](slice []T, initial U, fn func(U, T) U) U {
    acc := initial
    for _, v := range slice {
        acc = fn(acc, v)
    }
    return acc
}

// Example: count words longer than 4 characters
words := []string{"go", "generics", "are", "powerful", "and", "elegant"}
longWords := Filter(words, func(w string) bool { return len(w) > 4 })
// longWords = ["generics", "powerful", "elegant"]

totalLen := Reduce(longWords, 0, func(acc int, w string) int { return acc + len(w) })
// totalLen = 7 + 8 + 7 = 22
```

### 1.4 Utility Functions: Must, Ptr

```go
// Must panics on error — only use for initialization code, not request handlers!
func Must[T any](val T, err error) T {
    if err != nil {
        panic(err)
    }
    return val
}

// Usage:
// db := Must(sql.Open("postgres", dsn))  // acceptable at startup

// Ptr returns a pointer to a value — useful for optional struct fields
func Ptr[T any](v T) *T {
    return &v
}

// Without Ptr you'd need:
//   s := "hello"
//   cfg.Name = &s
// With Ptr:
//   cfg.Name = Ptr("hello")
```

### 1.5 Generic Types

```go
// Stack[T] is a type-safe stack.
type Stack[T any] struct {
    items []T
}

func (s *Stack[T]) Push(item T) {
    s.items = append(s.items, item)
}

func (s *Stack[T]) Pop() (T, bool) {
    if len(s.items) == 0 {
        var zero T
        return zero, false
    }
    top := s.items[len(s.items)-1]
    s.items = s.items[:len(s.items)-1]
    return top, true
}

func (s *Stack[T]) Peek() (T, bool) {
    if len(s.items) == 0 {
        var zero T
        return zero, false
    }
    return s.items[len(s.items)-1], true
}

func (s *Stack[T]) Len() int { return len(s.items) }

// Result[T] models either a value or an error — Railway-oriented programming style.
type Result[T any] struct {
    value T
    err   error
}

func Ok[T any](v T) Result[T]      { return Result[T]{value: v} }
func Err[T any](e error) Result[T] { return Result[T]{err: e} }

func (r Result[T]) Unwrap() (T, error) { return r.value, r.err }

func (r Result[T]) Or(defaultVal T) T {
    if r.err != nil {
        return defaultVal
    }
    return r.value
}

// Optional[T] represents a value that may or may not be present.
type Optional[T any] struct {
    value   T
    present bool
}

func Some[T any](v T) Optional[T]  { return Optional[T]{value: v, present: true} }
func None[T any]() Optional[T]     { return Optional[T]{} }

func (o Optional[T]) IsSome() bool { return o.present }

func (o Optional[T]) Get() (T, bool) { return o.value, o.present }

func (o Optional[T]) OrElse(defaultVal T) T {
    if o.present {
        return o.value
    }
    return defaultVal
}
```

### 1.6 When NOT to Use Generics

Generics add complexity. Use them when:
- You're writing truly reusable data structures (Stack, Queue, Set, Cache)
- You're writing utility functions that operate on slices/maps of any type
- You want compile-time type safety instead of `interface{}`

Do NOT use generics when:
- The function already works well with a specific type
- You'd need complex constraint hierarchies just for one use case
- Reflection or interface{} is clearer and the performance difference doesn't matter
- You're writing domain business logic (generics are for infrastructure/utilities)

```go
// BAD: this adds no value — just write it for your specific type
func GetID[T interface{ GetID() string }](item T) string {
    return item.GetID()
}

// GOOD: just use the interface directly
type IDGetter interface {
    GetID() string
}
func GetID(item IDGetter) string {
    return item.GetID()
}
```

---

## Section 2: Testing Patterns

### 2.1 Table-Driven Tests — The Go Standard

```go
// calculator/calculator_test.go
package calculator_test

import (
    "testing"
    "github.com/yourorg/calculator"
)

func TestDivide(t *testing.T) {
    // Define all test cases in a slice — easy to add cases, easy to read failures
    tests := []struct {
        name    string
        a, b    float64
        want    float64
        wantErr bool
    }{
        {
            name: "positive numbers",
            a: 10, b: 2,
            want: 5,
        },
        {
            name: "divide by zero",
            a: 5, b: 0,
            wantErr: true,
        },
        {
            name: "negative result",
            a: -10, b: 2,
            want: -5,
        },
        {
            name: "fractional result",
            a: 1, b: 3,
            want: 0.3333333333333333,
        },
    }

    for _, tt := range tests {
        // t.Run creates a subtest. Name appears in failure: "TestDivide/positive_numbers"
        t.Run(tt.name, func(t *testing.T) {
            // t.Parallel() lets subtests run concurrently — great for slow tests.
            // Don't use on tests that modify shared state.
            t.Parallel()

            got, err := calculator.Divide(tt.a, tt.b)

            if (err != nil) != tt.wantErr {
                t.Errorf("Divide() error = %v, wantErr %v", err, tt.wantErr)
                return
            }
            if !tt.wantErr && got != tt.want {
                t.Errorf("Divide() = %v, want %v", got, tt.want)
            }
        })
    }
}
```

### 2.2 Benchmarks

```go
// strings/concat_test.go
package strings_test

import (
    "fmt"
    "strings"
    "testing"
)

// Benchmark: string concatenation with + vs strings.Builder
// Run with: go test -bench=. -benchmem -count=3

func BenchmarkConcatPlus(b *testing.B) {
    // b.N is automatically tuned by the benchmark runner
    for i := 0; i < b.N; i++ {
        result := ""
        for j := 0; j < 100; j++ {
            result += fmt.Sprintf("item-%d", j)
        }
        _ = result
    }
}

func BenchmarkConcatBuilder(b *testing.B) {
    for i := 0; i < b.N; i++ {
        var sb strings.Builder
        for j := 0; j < 100; j++ {
            fmt.Fprintf(&sb, "item-%d", j)
        }
        _ = sb.String()
    }
}

func BenchmarkConcatBuilderPreallocated(b *testing.B) {
    for i := 0; i < b.N; i++ {
        var sb strings.Builder
        sb.Grow(800) // pre-allocate ~800 bytes
        for j := 0; j < 100; j++ {
            fmt.Fprintf(&sb, "item-%d", j)
        }
        _ = sb.String()
    }
}

// Parallel benchmark: simulates concurrent load
func BenchmarkConcatBuilderParallel(b *testing.B) {
    b.RunParallel(func(pb *testing.PB) {
        for pb.Next() {
            var sb strings.Builder
            sb.Grow(800)
            for j := 0; j < 100; j++ {
                fmt.Fprintf(&sb, "item-%d", j)
            }
            _ = sb.String()
        }
    })
}

// b.ResetTimer: exclude setup code from the benchmark time
func BenchmarkWithExpensiveSetup(b *testing.B) {
    data := generateLargeDataset() // expensive, but not what we're measuring
    b.ResetTimer()                  // start timing here

    for i := 0; i < b.N; i++ {
        processDataset(data)
    }
}
```

Typical results (approximate):

```
BenchmarkConcatPlus-8                    5000    234567 ns/op    45232 B/op    99 allocs/op
BenchmarkConcatBuilder-8                50000     23456 ns/op     4096 B/op     8 allocs/op
BenchmarkConcatBuilderPreallocated-8   100000     12345 ns/op     1024 B/op     1 allocs/op
```

`-benchmem` adds allocation stats: `B/op` (bytes allocated per operation) and `allocs/op` (allocations per operation). Reducing allocs is often more impactful than reducing raw CPU time.

### 2.3 Fuzz Testing (Go 1.18+)

```go
// parser/parser_test.go
package parser_test

import (
    "testing"
    "github.com/yourorg/parser"
)

// Fuzz test: the runtime generates random inputs to find crashes/panics.
// Run for 30 seconds: go test -fuzz=FuzzParseURL -fuzztime=30s
// Run to verify existing corpus: go test (runs all FuzzXxx as normal tests with seed corpus)
func FuzzParseURL(f *testing.F) {
    // Seed corpus: known-good inputs the fuzzer starts with
    f.Add("https://example.com/path?q=1")
    f.Add("http://localhost:8080")
    f.Add("")
    f.Add("not-a-url")

    f.Fuzz(func(t *testing.T, rawURL string) {
        // Any panic here is a bug.
        // The fuzzer reports the minimal input that causes the crash.
        result, err := parser.ParseURL(rawURL)
        if err != nil {
            return // errors are fine; panics are not
        }
        // Invariant: round-trip should be stable
        result2, err := parser.ParseURL(result.String())
        if err != nil {
            t.Errorf("round-trip failed for %q: %v", result.String(), err)
        }
        if result.String() != result2.String() {
            t.Errorf("round-trip unstable: %q -> %q -> %q",
                rawURL, result.String(), result2.String())
        }
    })
}
```

### 2.4 Golden Files (Testdata)

```go
// report/report_test.go
package report_test

import (
    "os"
    "path/filepath"
    "testing"
    "github.com/yourorg/report"
)

// Golden file test: compare output against a known-good file.
// Update goldens: go test -update-golden
func TestGenerateReport(t *testing.T) {
    updateGolden := os.Getenv("UPDATE_GOLDEN") == "1"

    input := report.Input{Title: "Q4 2024", Items: []string{"Revenue: $1M", "Users: 50K"}}
    got := report.Generate(input)

    goldenPath := filepath.Join("testdata", "q4_report.golden")

    if updateGolden {
        // Write current output as the new golden file
        os.MkdirAll("testdata", 0755)
        if err := os.WriteFile(goldenPath, []byte(got), 0644); err != nil {
            t.Fatalf("write golden: %v", err)
        }
        t.Logf("Golden file updated: %s", goldenPath)
        return
    }

    want, err := os.ReadFile(goldenPath)
    if err != nil {
        t.Fatalf("read golden file (run UPDATE_GOLDEN=1 go test to create it): %v", err)
    }

    if got != string(want) {
        t.Errorf("output mismatch:\ngot:\n%s\nwant:\n%s", got, want)
    }
}
```

### 2.5 HTTP Testing

```go
// api/handler_test.go
package api_test

import (
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"
    "github.com/yourorg/api"
)

func TestCreateUser_Handler(t *testing.T) {
    // httptest.NewRecorder captures the response without starting a real server
    handler := api.NewHandler(api.NewInMemoryUserStore())

    body := `{"name":"Alice","email":"alice@example.com"}`
    req := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(body))
    req.Header.Set("Content-Type", "application/json")

    rr := httptest.NewRecorder()
    handler.ServeHTTP(rr, req)

    if rr.Code != http.StatusCreated {
        t.Errorf("expected 201, got %d; body: %s", rr.Code, rr.Body.String())
    }

    var resp map[string]interface{}
    if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
        t.Fatalf("unmarshal response: %v", err)
    }

    if resp["email"] != "alice@example.com" {
        t.Errorf("unexpected email: %v", resp["email"])
    }
}

// httptest.NewServer starts a real HTTP server — use for testing HTTP clients
func TestHTTPClient(t *testing.T) {
    ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/users/1" {
            http.NotFound(w, r)
            return
        }
        w.Header().Set("Content-Type", "application/json")
        json.NewEncoder(w).Encode(map[string]string{"id": "1", "name": "Alice"})
    }))
    defer ts.Close() // always defer Close() to free the port

    client := api.NewUserClient(ts.URL)
    user, err := client.GetUser("1")
    if err != nil {
        t.Fatalf("GetUser: %v", err)
    }
    if user.Name != "Alice" {
        t.Errorf("expected Alice, got %s", user.Name)
    }
}
```

### 2.6 Mocking — No Framework Needed for Simple Cases

```go
// The Go way: define an interface, provide a fake implementation in tests.
// No code generation, no reflection, no magic.

// In your production code:
type UserRepository interface {
    GetByID(ctx context.Context, id string) (*User, error)
    Save(ctx context.Context, user *User) error
}

// In your test file:
type mockUserRepo struct {
    users map[string]*User
    saveErr error
}

func newMockUserRepo() *mockUserRepo {
    return &mockUserRepo{users: make(map[string]*User)}
}

func (m *mockUserRepo) GetByID(ctx context.Context, id string) (*User, error) {
    u, ok := m.users[id]
    if !ok {
        return nil, ErrNotFound
    }
    return u, nil
}

func (m *mockUserRepo) Save(ctx context.Context, user *User) error {
    if m.saveErr != nil {
        return m.saveErr
    }
    m.users[user.ID] = user
    return nil
}

func TestUserService_CreateUser(t *testing.T) {
    repo := newMockUserRepo()
    repo.users["existing@test.com"] = &User{Email: "existing@test.com"}

    svc := NewUserService(repo)
    _, err := svc.CreateUser(context.Background(), "existing@test.com")

    if !errors.Is(err, ErrEmailTaken) {
        t.Errorf("expected ErrEmailTaken, got %v", err)
    }
}
```

### 2.7 testify/mock — For Complex Assertion Scenarios

```go
// go get github.com/stretchr/testify/mock
import "github.com/stretchr/testify/mock"

// Step 1: Define the mock struct
type MockEmailSender struct {
    mock.Mock
}

// Step 2: Implement each interface method using mock.Called
func (m *MockEmailSender) Send(to, subject, body string) error {
    args := m.Called(to, subject, body)
    return args.Error(0)
}

// Step 3: In your test, set up expectations
func TestNotificationService_SendsEmail(t *testing.T) {
    sender := new(MockEmailSender)

    // Expect Send to be called with these exact args, return nil (no error)
    sender.On("Send", "alice@example.com", mock.AnythingOfType("string"), mock.Anything).
        Return(nil).
        Once() // expect exactly one call

    svc := NewNotificationService(sender)
    err := svc.NotifyOrderCreated("alice@example.com", "order-123")

    if err != nil {
        t.Errorf("unexpected error: %v", err)
    }

    // Assert all expectations were met
    sender.AssertExpectations(t)
}
```

### 2.8 Coverage

```bash
# Run tests with coverage
go test ./... -cover

# Generate HTML report
go test ./... -coverprofile=coverage.out
go tool cover -html=coverage.out -o coverage.html
open coverage.html

# Show per-function coverage
go tool cover -func=coverage.out | tail -5

# Fail if coverage drops below 80%
go test ./... -coverprofile=coverage.out
go tool cover -func=coverage.out | awk '/^total/ { if ($3+0 < 80) { print "Coverage " $3 " below 80%"; exit 1 } }'
```

---

## Section 3: Performance & Profiling

### 3.1 pprof — CPU Profiling

```go
// Add to your main.go or a separate profiling endpoint:
import _ "net/http/pprof"

func main() {
    // Expose pprof endpoints on a separate port (never expose to the internet!)
    go func() {
        log.Println(http.ListenAndServe("localhost:6060", nil))
    }()
    // ... rest of main
}
```

Collect and analyze:

```bash
# 30-second CPU profile
go tool pprof http://localhost:6060/debug/pprof/profile?seconds=30

# Memory (heap) profile
go tool pprof http://localhost:6060/debug/pprof/heap

# Goroutine dump — find goroutine leaks
go tool pprof http://localhost:6060/debug/pprof/goroutine

# Inside pprof interactive mode:
# top10          — top 10 functions by CPU time
# top10 -cum     — top 10 by cumulative time (includes callees)
# list FuncName  — annotated source for a specific function
# web            — open flame graph in browser (requires graphviz)
```

### 3.2 In-Test Profiling with Benchmarks

```go
// Run and automatically generate a CPU profile:
// go test -bench=BenchmarkHotPath -cpuprofile=cpu.prof -memprofile=mem.prof
// go tool pprof cpu.prof

func BenchmarkHotPath(b *testing.B) {
    data := makeTestData(1000)
    b.ResetTimer()

    for i := 0; i < b.N; i++ {
        _ = processHotPath(data)
    }
}
```

### 3.3 Escape Analysis — Reducing Allocations

The compiler allocates values on the stack (fast, automatically freed) or heap (slower, GC pressure). Use escape analysis to understand why allocations happen:

```bash
go build -gcflags="-m" ./... 2>&1 | head -30

# More verbose:
go build -gcflags="-m -m" ./...
```

Example output:
```
./main.go:15:13: &User{...} escapes to heap   ← heap allocation (GC pressure)
./main.go:22:12: user does not escape          ← stack allocation (fast)
```

Common causes of heap escape:
- Returning a pointer to a local variable
- Storing a value in an interface
- Capturing a variable in a closure
- Value is too large for the stack

```go
// ALLOCATES on heap (escapes): returning pointer causes escape
func newUser(name string) *User {
    return &User{Name: name} // escapes to heap
}

// STACK allocated: caller passes in the storage
func fillUser(u *User, name string) {
    u.Name = name // u was allocated by the caller; this doesn't cause escape
}

// To avoid interface-based escape in hot paths:
// Instead of:
func process(v interface{}) {} // v escapes to heap

// Use a concrete type or a typed function:
func processUser(u *User) {} // no escape
```

### 3.4 sync.Pool — Reuse Objects to Reduce GC Pressure

```go
import "sync"

// Pool for reusing byte buffers in a high-throughput path.
var bufferPool = sync.Pool{
    New: func() interface{} {
        // Called when pool is empty — return a pointer, not a value
        buf := make([]byte, 0, 4096)
        return &buf
    },
}

func processRequest(data []byte) []byte {
    // Get a buffer from the pool
    bufPtr := bufferPool.Get().(*[]byte)
    buf := (*bufPtr)[:0] // reset length, keep capacity

    defer func() {
        // Return to pool — zero it first if it contains sensitive data
        *bufPtr = buf[:0]
        bufferPool.Put(bufPtr)
    }()

    // Use buf for processing...
    buf = append(buf, data...)
    buf = append(buf, " processed"...)

    // Must copy before returning — the buffer will be reused
    result := make([]byte, len(buf))
    copy(result, buf)
    return result
}
```

**Important:** sync.Pool items can be GC'd at any time. Never use it for items that must persist across GC cycles. It's for ephemeral, recyclable objects like buffers.

### 3.5 Pre-allocation

```go
// BAD: grows slice repeatedly, many allocations
func badSlice(n int) []int {
    var result []int  // starts at nil, cap=0
    for i := 0; i < n; i++ {
        result = append(result, i)  // potentially allocates on every call
    }
    return result
}

// GOOD: one allocation
func goodSlice(n int) []int {
    result := make([]int, 0, n)  // pre-allocate capacity
    for i := 0; i < n; i++ {
        result = append(result, i)  // never reallocates
    }
    return result
}

// BAD: map grows dynamically
func badMap(keys []string) map[string]int {
    m := make(map[string]int)  // starts tiny
    for i, k := range keys {
        m[k] = i  // may rehash multiple times
    }
    return m
}

// GOOD: pre-size the map
func goodMap(keys []string) map[string]int {
    m := make(map[string]int, len(keys))  // hint: expect len(keys) entries
    for i, k := range keys {
        m[k] = i
    }
    return m
}

// Benchmark shows the difference:
// BenchmarkBadSlice-8     500000    3421 ns/op   8192 B/op   12 allocs/op
// BenchmarkGoodSlice-8   2000000     712 ns/op   4096 B/op    1 allocs/op
```

---

## Section 4: Build & Distribution

### 4.1 Build Constraints

```go
// Only compile this file on Linux with AMD64 architecture
//go:build linux && amd64

package platform

func platformName() string { return "linux/amd64" }
```

```go
// Exclude from test builds
//go:build !test

package main
```

```go
// Multiple tags
//go:build (linux || darwin) && !386

package net
```

### 4.2 Cross-Compilation

```bash
# Build for Linux on any machine (including macOS/Windows)
GOOS=linux GOARCH=amd64 go build -o bin/app-linux-amd64 .

# Build for ARM64 (Apple Silicon, AWS Graviton, Raspberry Pi)
GOOS=linux GOARCH=arm64 go build -o bin/app-linux-arm64 .

# Build for Windows
GOOS=windows GOARCH=amd64 go build -o bin/app-windows.exe .

# Build for macOS
GOOS=darwin GOARCH=arm64 go build -o bin/app-macos-arm64 .

# List all supported targets
go tool dist list
```

### 4.3 go:embed — Embed Static Files at Compile Time

```go
package main

import (
    "embed"
    "html/template"
    "net/http"
)

// Embed a single file
//go:embed static/index.html
var indexHTML []byte

// Embed an entire directory tree
//go:embed static/*
var staticFiles embed.FS

// Embed templates
//go:embed templates/*.html
var templateFS embed.FS

func main() {
    // Serve embedded files
    http.Handle("/static/", http.FileServer(http.FS(staticFiles)))

    // Parse embedded templates
    tmpl := template.Must(template.ParseFS(templateFS, "templates/*.html"))

    http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
        tmpl.ExecuteTemplate(w, "index.html", nil)
    })

    http.ListenAndServe(":8080", nil)
}
```

### 4.4 ldflags — Inject Version at Build Time

```go
// main.go
package main

import "fmt"

// These are set by the linker via -ldflags
var (
    version   = "dev"
    commit    = "none"
    buildDate = "unknown"
)

func printVersion() {
    fmt.Printf("Version:    %s\nCommit:     %s\nBuild Date: %s\n",
        version, commit, buildDate)
}
```

```bash
# Inject version info at build time
go build \
  -ldflags "-X main.version=1.2.3 -X main.commit=$(git rev-parse --short HEAD) -X main.buildDate=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -o myapp .

# -w: strip DWARF debug info (smaller binary)
# -s: strip symbol table (smaller binary)
go build -ldflags "-w -s -X main.version=1.2.3" -o myapp .
```

### 4.5 Makefile for Go Projects

```makefile
# Makefile
BINARY_NAME := myapp
VERSION     := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT      := $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
BUILD_DATE  := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS     := -ldflags "-w -s -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildDate=$(BUILD_DATE)"

.PHONY: all build test lint clean docker run

all: lint test build

build:
	CGO_ENABLED=0 go build $(LDFLAGS) -o bin/$(BINARY_NAME) ./cmd/$(BINARY_NAME)

# Build for all platforms
build-all:
	GOOS=linux  GOARCH=amd64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-linux-amd64  .
	GOOS=linux  GOARCH=arm64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-linux-arm64  .
	GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-darwin-arm64 .

test:
	go test ./... -race -cover -coverprofile=coverage.out
	go tool cover -func=coverage.out | grep -E "^total"

test-integration:
	go test ./... -tags integration -race

bench:
	go test ./... -bench=. -benchmem -run=^$

lint:
	golangci-lint run ./...

clean:
	rm -rf bin/ coverage.out

docker:
	docker build -t $(BINARY_NAME):$(VERSION) .

run: build
	./bin/$(BINARY_NAME)

# Generate protobuf code
proto:
	protoc --go_out=. --go_opt=paths=source_relative \
	       --go-grpc_out=. --go-grpc_opt=paths=source_relative \
	       proto/**/*.proto

# go:generate — run all //go:generate directives in the package
generate:
	go generate ./...
```

### 4.6 go:generate

```go
// In your source file, add a //go:generate comment:

//go:generate protoc --go_out=. --go-grpc_out=. proto/user/user.proto
//go:generate mockgen -source=repository.go -destination=mocks/repository_mock.go
//go:generate stringer -type=Status

// Then run: go generate ./...
// This executes the commands in the comments.
```

---

## Section 5: Patterns & Best Practices

### 5.1 Functional Options Pattern

The most idiomatic way to handle optional configuration in Go:

```go
// server.go
package server

import (
    "log/slog"
    "net/http"
    "time"
)

type Server struct {
    addr         string
    readTimeout  time.Duration
    writeTimeout time.Duration
    maxBodyBytes int64
    logger       *slog.Logger
    middleware   []func(http.Handler) http.Handler
}

// Option is a function that configures a Server.
type Option func(*Server)

// WithAddr sets the listening address.
func WithAddr(addr string) Option {
    return func(s *Server) {
        s.addr = addr
    }
}

// WithReadTimeout sets the HTTP read timeout.
func WithReadTimeout(d time.Duration) Option {
    return func(s *Server) {
        s.readTimeout = d
    }
}

// WithWriteTimeout sets the HTTP write timeout.
func WithWriteTimeout(d time.Duration) Option {
    return func(s *Server) {
        s.writeTimeout = d
    }
}

// WithLogger sets the logger.
func WithLogger(logger *slog.Logger) Option {
    return func(s *Server) {
        s.logger = logger
    }
}

// WithMiddleware adds a middleware to the chain.
func WithMiddleware(mw func(http.Handler) http.Handler) Option {
    return func(s *Server) {
        s.middleware = append(s.middleware, mw)
    }
}

// NewServer creates a Server with sensible defaults, then applies options.
func NewServer(opts ...Option) *Server {
    // Start with defaults
    s := &Server{
        addr:         ":8080",
        readTimeout:  15 * time.Second,
        writeTimeout: 15 * time.Second,
        maxBodyBytes: 1 << 20, // 1MB
        logger:       slog.Default(),
    }

    // Apply each option
    for _, opt := range opts {
        opt(s)
    }

    return s
}

// Usage:
// srv := NewServer(
//     WithAddr(":9090"),
//     WithReadTimeout(30 * time.Second),
//     WithLogger(myLogger),
//     WithMiddleware(authMiddleware),
//     WithMiddleware(loggingMiddleware),
// )
```

**Why functional options over a config struct?**
- Zero values are valid (no confusion between "not set" vs "set to zero")
- Easy to add new options without breaking existing callers
- Options are self-documenting
- Options can be composed and reused

### 5.2 Repository Pattern

```go
// Domain interface — defined alongside the domain type, NOT the storage layer
type UserRepository interface {
    GetByID(ctx context.Context, id string) (*User, error)
    GetByEmail(ctx context.Context, email string) (*User, error)
    Save(ctx context.Context, user *User) error
    Delete(ctx context.Context, id string) error
    List(ctx context.Context, filter UserFilter) ([]*User, error)
}

// PostgreSQL implementation
type postgresUserRepo struct {
    db *sql.DB
}

func NewPostgresUserRepo(db *sql.DB) UserRepository {
    return &postgresUserRepo{db: db}
}

func (r *postgresUserRepo) GetByID(ctx context.Context, id string) (*User, error) {
    const q = `SELECT id, name, email, created_at FROM users WHERE id = $1`

    var u User
    err := r.db.QueryRowContext(ctx, q, id).Scan(&u.ID, &u.Name, &u.Email, &u.CreatedAt)
    if errors.Is(err, sql.ErrNoRows) {
        return nil, ErrNotFound
    }
    if err != nil {
        return nil, fmt.Errorf("get user by id: %w", err)
    }
    return &u, nil
}

// In-memory implementation for tests — implement the same interface
type inMemoryUserRepo struct {
    mu    sync.RWMutex
    users map[string]*User
}

func NewInMemoryUserRepo() UserRepository {
    return &inMemoryUserRepo{users: make(map[string]*User)}
}

func (r *inMemoryUserRepo) GetByID(ctx context.Context, id string) (*User, error) {
    r.mu.RLock()
    defer r.mu.RUnlock()
    u, ok := r.users[id]
    if !ok {
        return nil, ErrNotFound
    }
    return u, nil
}

// Service layer depends on the interface, not the implementation
type UserService struct {
    repo   UserRepository // interface — testable, swappable
    mailer Mailer
    logger *slog.Logger
}

func NewUserService(repo UserRepository, mailer Mailer, logger *slog.Logger) *UserService {
    return &UserService{repo: repo, mailer: mailer, logger: logger}
}
```

### 5.3 Dependency Injection (Manual DI)

```go
// Wire everything together in main.go (the composition root).
// Don't use a DI framework unless the codebase is very large.

func main() {
    // 1. Infrastructure
    db := mustOpenDB(os.Getenv("DATABASE_URL"))
    logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

    // 2. Repositories
    userRepo := NewPostgresUserRepo(db)
    orderRepo := NewPostgresOrderRepo(db)

    // 3. Services (inject repositories)
    emailSender := NewSMTPSender(os.Getenv("SMTP_HOST"))
    userSvc := NewUserService(userRepo, emailSender, logger)
    orderSvc := NewOrderService(orderRepo, userSvc, logger)

    // 4. Handlers (inject services)
    userHandler := NewUserHandler(userSvc)
    orderHandler := NewOrderHandler(orderSvc)

    // 5. Router
    mux := http.NewServeMux()
    mux.Handle("/users", userHandler)
    mux.Handle("/orders", orderHandler)

    // 6. Run
    http.ListenAndServe(":8080", mux)
}
```

For large apps with hundreds of dependencies, consider **wire** (google/wire): a code generator that validates your dependency graph at compile time.

### 5.4 Graceful Shutdown

```go
// Complete, production-ready graceful shutdown pattern.
func run() error {
    ctx, stop := signal.NotifyContext(context.Background(),
        syscall.SIGINT,
        syscall.SIGTERM,
    )
    defer stop()

    // --- start all servers/workers ---

    httpServer := &http.Server{
        Addr:    ":8080",
        Handler: buildRouter(),
    }

    kafkaConsumer := NewConsumer(...)

    var g errgroup.Group // golang.org/x/sync/errgroup

    // HTTP server
    g.Go(func() error {
        slog.Info("HTTP server starting", "addr", httpServer.Addr)
        if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
            return fmt.Errorf("http server: %w", err)
        }
        return nil
    })

    // Kafka consumer
    g.Go(func() error {
        return kafkaConsumer.Run(ctx) // returns when ctx is done
    })

    // Shutdown trigger
    g.Go(func() error {
        <-ctx.Done() // wait for signal
        slog.Info("shutdown signal received")

        // Give in-flight requests 30 seconds to finish
        shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
        defer cancel()

        if err := httpServer.Shutdown(shutdownCtx); err != nil {
            slog.Error("HTTP server shutdown error", "error", err)
        }
        return nil
    })

    return g.Wait()
}
```

### 5.5 Structured Logging with slog (Go 1.21+)

```go
// slog is the standard library's structured logger — prefer it over third-party loggers.
import "log/slog"

// Create loggers
jsonLogger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
    Level:     slog.LevelInfo,
    AddSource: true, // adds file:line to each log entry
}))

textLogger := slog.New(slog.NewTextHandler(os.Stderr, nil)) // for local dev

// Set the default logger globally
slog.SetDefault(jsonLogger)

// Log with structured attributes
slog.Info("user created",
    "user_id", "user-123",
    "email", "alice@example.com",
    slog.Duration("elapsed", time.Since(start)),
    slog.Int("attempt", 3),
)

// Group related fields
slog.Info("request completed",
    slog.Group("request",
        slog.String("method", r.Method),
        slog.String("path", r.URL.Path),
        slog.Int("status", 200),
    ),
    slog.Group("performance",
        slog.Duration("duration", elapsed),
        slog.Int64("bytes_written", bytesWritten),
    ),
)

// Logger with pre-set fields (like zap's With())
requestLogger := slog.With(
    "request_id", requestID,
    "user_id", userID,
)
requestLogger.Info("processing order", "order_id", orderID)
requestLogger.Error("payment failed", "error", err)
```

### 5.6 Error Wrapping Strategies

```go
// RULE 1: Wrap errors at the boundary where you add context.
// Don't double-wrap — if the caller already has enough context, don't add more.

// GOOD: wrap at the repository layer, not again in the service layer
func (r *postgresUserRepo) GetByID(ctx context.Context, id string) (*User, error) {
    var u User
    err := r.db.QueryRowContext(ctx, q, id).Scan(...)
    if err != nil {
        return nil, fmt.Errorf("postgres: get user %s: %w", id, err)
        //                      ↑ add concrete context   ↑ %w preserves error chain
    }
    return &u, nil
}

// RULE 2: Define sentinel errors for conditions callers need to handle differently
var (
    ErrNotFound     = errors.New("not found")
    ErrUnauthorized = errors.New("unauthorized")
    ErrConflict     = errors.New("conflict")
)

// RULE 3: Define error types for errors that carry data
type ValidationError struct {
    Field   string
    Message string
}
func (e *ValidationError) Error() string {
    return fmt.Sprintf("validation failed: %s: %s", e.Field, e.Message)
}

// RULE 4: Use errors.Is and errors.As — they traverse the wrap chain
if errors.Is(err, ErrNotFound) {
    // handles both unwrapped ErrNotFound AND any error wrapping it
    http.NotFound(w, r)
    return
}

var ve *ValidationError
if errors.As(err, &ve) {
    // extracted the ValidationError from the chain
    respondError(w, http.StatusBadRequest, ve.Message)
    return
}

// RULE 5: Don't wrap errors that are already self-descriptive
// BAD: wrapping io.EOF adds nothing
return fmt.Errorf("read: %w", io.EOF) // caller already knows io.EOF means end of stream

// RULE 6: Never discard errors silently
// BAD:
f, _ := os.Create(path)

// GOOD:
f, err := os.Create(path)
if err != nil {
    return fmt.Errorf("create output file: %w", err)
}
```

### 5.7 Context Guidelines

```go
// RULE 1: Context is always the first parameter, named ctx
func GetUser(ctx context.Context, id string) (*User, error) { ... }

// RULE 2: NEVER store context in a struct
// BAD:
type BadService struct {
    ctx context.Context // NEVER do this
    db  *sql.DB
}

// GOOD: pass ctx to each method that needs it
type GoodService struct {
    db *sql.DB
}
func (s *GoodService) GetUser(ctx context.Context, id string) (*User, error) { ... }

// RULE 3: Use context.WithTimeout for external calls (DB, HTTP, gRPC)
func (s *Service) FetchRemoteData(ctx context.Context) ([]byte, error) {
    reqCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
    defer cancel() // ALWAYS defer cancel — prevents context leak

    resp, err := http.NewRequestWithContext(reqCtx, "GET", url, nil)
    ...
}

// RULE 4: Use context.Value only for request-scoped data, not for passing function params
// Acceptable: request ID, trace ID, authenticated user (read-only metadata)
// NOT acceptable: database connection, logger, config values

type contextKey string
const RequestIDKey contextKey = "request_id"

func WithRequestID(ctx context.Context, id string) context.Context {
    return context.WithValue(ctx, RequestIDKey, id)
}

func GetRequestID(ctx context.Context) string {
    id, _ := ctx.Value(RequestIDKey).(string)
    return id
}

// RULE 5: Check ctx.Done() in long-running loops
func processItems(ctx context.Context, items []Item) error {
    for _, item := range items {
        select {
        case <-ctx.Done():
            return ctx.Err() // client disconnected or deadline exceeded
        default:
        }
        if err := processItem(ctx, item); err != nil {
            return err
        }
    }
    return nil
}
```

---

## Section 6: Common Go Anti-Patterns to Avoid

### Anti-Pattern 1: Returning Concrete Types Instead of Interfaces

```go
// BAD: callers are now tightly coupled to PostgresUserStore
func NewUserStore(db *sql.DB) *PostgresUserStore {
    return &PostgresUserStore{db: db}
}

// GOOD: return the interface — callers work with the abstraction
// Exception: constructors can return concrete types if the type IS the abstraction
func NewUserStore(db *sql.DB) UserRepository {
    return &postgresUserStore{db: db}
}

// The Go proverb: "Accept interfaces, return concrete types"
// This means function PARAMETERS should be interfaces (accept any implementation).
// Return types can be concrete when the type itself expresses the abstraction.
```

### Anti-Pattern 2: Ignoring Errors

```go
// BAD: silent failure, production incidents guaranteed
rows, _ := db.QueryContext(ctx, q)
f, _ := os.Create("/tmp/output")
json.Unmarshal(data, &result) // no error check

// GOOD: always handle errors
rows, err := db.QueryContext(ctx, q)
if err != nil {
    return fmt.Errorf("query users: %w", err)
}

// If you genuinely don't care about an error, document why:
_ = f.Close() // best-effort: if close fails, data is already written
```

### Anti-Pattern 3: Global State

```go
// BAD: global variables make code untestable and cause data races
var db *sql.DB
var logger *slog.Logger

func GetUser(id string) (*User, error) {
    return db.Query("SELECT ...") // which db? configured how? testable?
}

// GOOD: explicit dependencies via struct fields
type UserService struct {
    db     *sql.DB
    logger *slog.Logger
}

func (s *UserService) GetUser(ctx context.Context, id string) (*User, error) {
    return s.db.QueryContext(ctx, "SELECT ...")
}
```

### Anti-Pattern 4: Goroutine Leaks

The top 5 goroutine leak patterns and how to fix them:

```go
// LEAK 1: goroutine blocked on channel send, receiver gone
func leaky1(done <-chan struct{}) {
    ch := make(chan int) // unbuffered
    go func() {
        // This goroutine leaks if no one reads from ch before done is closed
        ch <- compute()
    }()
    select {
    case result := <-ch:
        use(result)
    case <-done:
        return // goroutine is now stuck trying to send!
    }
}
// FIX: use buffered channel or pass ctx to the goroutine
func fixed1(ctx context.Context) {
    ch := make(chan int, 1) // buffered — goroutine can always send
    go func() {
        select {
        case ch <- compute():
        case <-ctx.Done():
        }
    }()
}

// LEAK 2: goroutine in infinite loop with no exit condition
func leaky2() {
    go func() {
        for { // runs forever!
            doWork()
        }
    }()
}
// FIX: check ctx.Done()
func fixed2(ctx context.Context) {
    go func() {
        for {
            select {
            case <-ctx.Done():
                return
            default:
                doWork()
            }
        }
    }()
}

// LEAK 3: goroutine waiting on a channel that's never closed/sent to
func leaky3() {
    ch := make(chan struct{})
    go func() {
        <-ch // blocks forever if sender panics or returns early
        doWork()
    }()
    // if we forget to close(ch)...
}
// FIX: always ensure the channel is eventually closed
func fixed3() {
    ch := make(chan struct{})
    go func() {
        <-ch
        doWork()
    }()
    defer close(ch)
}

// LEAK 4: http.Client with no timeout — goroutine waits for response forever
func leaky4() {
    client := &http.Client{} // no timeout!
    resp, _ := client.Get("https://slow-server.example.com")
    _ = resp
}
// FIX: always set timeouts
func fixed4() {
    client := &http.Client{Timeout: 10 * time.Second}
    req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
    resp, err := client.Do(req)
    if err != nil { return }
    defer resp.Body.Close()
}

// LEAK 5: ticker not stopped
func leaky5() {
    ticker := time.NewTicker(time.Second)
    go func() {
        for range ticker.C {
            doPeriodicWork()
        }
    }()
    // ticker.Stop() never called — channel sends forever, goroutine lives forever
}
// FIX: stop the ticker, close done channel
func fixed5(ctx context.Context) {
    ticker := time.NewTicker(time.Second)
    go func() {
        defer ticker.Stop()
        for {
            select {
            case <-ctx.Done():
                return
            case <-ticker.C:
                doPeriodicWork()
            }
        }
    }()
}
```

### Anti-Pattern 5: Channels When a Mutex Is Simpler

```go
// BAD: over-engineered use of channels for a simple counter
type Counter struct {
    inc  chan struct{}
    get  chan chan int
    done chan struct{}
}

func NewCounter() *Counter {
    c := &Counter{
        inc:  make(chan struct{}),
        get:  make(chan chan int),
        done: make(chan struct{}),
    }
    go func() {
        val := 0
        for {
            select {
            case <-c.inc:
                val++
            case ch := <-c.get:
                ch <- val
            case <-c.done:
                return
            }
        }
    }()
    return c
}

// GOOD: mutex is the right tool here
type Counter struct {
    mu  sync.Mutex
    val int
}

func (c *Counter) Inc() {
    c.mu.Lock()
    c.val++
    c.mu.Unlock()
}

func (c *Counter) Get() int {
    c.mu.Lock()
    defer c.mu.Unlock()
    return c.val
}

// Or even simpler for a counter:
type Counter struct {
    val atomic.Int64
}

func (c *Counter) Inc()      { c.val.Add(1) }
func (c *Counter) Get() int64 { return c.val.Load() }

// Go proverb: "Use channels to communicate, use mutexes to protect shared state."
// Channels are great for: pipelines, fan-out/fan-in, coordinating goroutine lifecycles.
// Mutexes are great for: protecting a shared data structure, simple state.
```

### Anti-Pattern 6: No Timeouts on HTTP Clients/Servers

```go
// BAD HTTP client — hangs indefinitely
client := &http.Client{}

// BAD HTTP server — hangs indefinitely on slow clients (Slowloris attack)
http.ListenAndServe(":8080", handler)

// GOOD HTTP client
client := &http.Client{
    Timeout: 30 * time.Second, // total request timeout
    Transport: &http.Transport{
        DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
        TLSHandshakeTimeout:   5 * time.Second,
        ResponseHeaderTimeout: 10 * time.Second,
        IdleConnTimeout:       90 * time.Second,
        MaxIdleConns:          100,
        MaxIdleConnsPerHost:   10,
    },
}

// GOOD HTTP server
server := &http.Server{
    Addr:              ":8080",
    Handler:           mux,
    ReadTimeout:       15 * time.Second, // time to read request headers+body
    ReadHeaderTimeout: 5 * time.Second,  // just headers (protects against Slowloris)
    WriteTimeout:      15 * time.Second, // time to write the response
    IdleTimeout:       60 * time.Second, // time between requests on keep-alive connection
    MaxHeaderBytes:    1 << 20,          // 1MB max header size
}
```

---

## Section 7: Configuration Management (12-Factor App)

```go
// config/config.go
// Load all config from environment variables.
// Never read env vars inside business logic — read them once at startup.
package config

import (
    "fmt"
    "os"
    "strconv"
    "time"
)

type Config struct {
    // Server
    HTTPPort    string
    GRPCPort    string
    Environment string

    // Database
    DatabaseURL     string
    DatabaseMaxConns int
    DatabaseTimeout  time.Duration

    // Kafka
    KafkaBrokers []string
    KafkaTopic   string

    // Observability
    LogLevel    string
    MetricsPort string

    // Security
    JWTSecret   string
    ServiceToken string
}

func Load() (*Config, error) {
    cfg := &Config{
        HTTPPort:         getEnv("HTTP_PORT", "8080"),
        GRPCPort:         getEnv("GRPC_PORT", "50051"),
        Environment:      getEnv("ENVIRONMENT", "development"),
        DatabaseURL:      mustGetEnv("DATABASE_URL"),
        DatabaseMaxConns: mustGetEnvInt("DATABASE_MAX_CONNS", 25),
        KafkaBrokers:     splitEnv("KAFKA_BROKERS", "localhost:9092"),
        KafkaTopic:       getEnv("KAFKA_TOPIC", "events"),
        LogLevel:         getEnv("LOG_LEVEL", "info"),
        JWTSecret:        mustGetEnv("JWT_SECRET"),
    }

    timeout, err := time.ParseDuration(getEnv("DATABASE_TIMEOUT", "5s"))
    if err != nil {
        return nil, fmt.Errorf("invalid DATABASE_TIMEOUT: %w", err)
    }
    cfg.DatabaseTimeout = timeout

    return cfg, nil
}

func getEnv(key, fallback string) string {
    if v := os.Getenv(key); v != "" {
        return v
    }
    return fallback
}

func mustGetEnv(key string) string {
    v := os.Getenv(key)
    if v == "" {
        panic(fmt.Sprintf("required environment variable %s is not set", key))
    }
    return v
}

func mustGetEnvInt(key string, fallback int) int {
    v := os.Getenv(key)
    if v == "" {
        return fallback
    }
    n, err := strconv.Atoi(v)
    if err != nil {
        panic(fmt.Sprintf("invalid value for %s: %v", key, err))
    }
    return n
}

func splitEnv(key, fallback string) []string {
    v := getEnv(key, fallback)
    return strings.Split(v, ",")
}
```

---

## Section 8: Quick Reference — Production Checklist

### Before merging code:
- [ ] All errors handled (no `_ = err`)
- [ ] All goroutines have an exit condition
- [ ] All timers/tickers are stopped
- [ ] External calls have timeouts
- [ ] No global mutable state
- [ ] Interfaces, not concrete types, as function parameters
- [ ] Context is first parameter in all functions that do I/O
- [ ] Tests cover the happy path and at least one error path
- [ ] `go vet ./...` passes
- [ ] `golangci-lint run` passes (or have an agreed lint config)

### Performance benchmarking workflow:
```bash
# 1. Establish a baseline
go test -bench=BenchmarkHotPath -benchmem -count=5 | tee baseline.txt

# 2. Make your change

# 3. Compare
go test -bench=BenchmarkHotPath -benchmem -count=5 | tee new.txt
go install golang.org/x/perf/cmd/benchstat@latest
benchstat baseline.txt new.txt
# Output shows statistical significance of the change
```

### The three questions for any optimization:
1. **Is it slow?** Measure first. Profile. Don't guess.
2. **Is it worth it?** Will users notice? Is this on the critical path?
3. **What's the trade-off?** More complex code, more memory, more maintenance burden?

As Rob Pike said: *"Measure. Don't tune for speed until you've measured, and even then don't unless one part of the code overwhelms the rest."*
