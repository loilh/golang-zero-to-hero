# Chapter 03 — Standard Library

Go ships with a rich standard library. This chapter covers the packages you'll use in almost every project, with real patterns and gotchas.

---

## 1. fmt — Formatted I/O

### Print Verbs

```go
type Point struct{ X, Y int }
p := Point{1, 2}

fmt.Printf("%v\n",  p)     // {1 2}           — default format
fmt.Printf("%+v\n", p)     // {X:1 Y:2}       — with field names
fmt.Printf("%#v\n", p)     // main.Point{X:1, Y:2} — Go syntax
fmt.Printf("%T\n",  p)     // main.Point       — type name

// Numbers
fmt.Printf("%d\n",  42)    // 42               — decimal int
fmt.Printf("%b\n",  42)    // 101010           — binary
fmt.Printf("%x\n",  42)    // 2a               — hex lowercase
fmt.Printf("%X\n",  42)    // 2A               — hex uppercase
fmt.Printf("%o\n",  42)    // 52               — octal
fmt.Printf("%e\n",  3.14)  // 3.140000e+00     — scientific
fmt.Printf("%f\n",  3.14)  // 3.140000         — decimal float
fmt.Printf("%.2f\n",3.14)  // 3.14             — 2 decimal places
fmt.Printf("%8.2f\n",3.14) // "    3.14"        — width 8, right aligned
fmt.Printf("%-8.2f\n",3.14)// "3.14    "        — left aligned

// Strings
fmt.Printf("%s\n",  "hello") // hello
fmt.Printf("%q\n",  "hello") // "hello"        — quoted
fmt.Printf("%10s\n","hi")    // "        hi"   — right padded

// Pointers
fmt.Printf("%p\n",  &p)   // 0xc000018060

// Boolean
fmt.Printf("%t\n",  true) // true
```

### Sprintf — Build Strings

```go
s := fmt.Sprintf("user %s has %d points", username, points)
msg := fmt.Sprintf("error at line %d: %v", lineNum, err)
```

### Stringer Interface

Implement `String() string` to control how your type prints:

```go
type Color int

const (
    Red Color = iota
    Green
    Blue
)

func (c Color) String() string {
    switch c {
    case Red:
        return "Red"
    case Green:
        return "Green"
    case Blue:
        return "Blue"
    default:
        return fmt.Sprintf("Color(%d)", int(c))
    }
}

c := Green
fmt.Println(c)       // Green (calls c.String())
fmt.Printf("%v\n", c) // Green
fmt.Printf("%d\n", c) // 1 (numeric verb bypasses Stringer)
```

### fmt.Errorf

```go
// Wrap errors with context (use %w for unwrappable errors):
err = fmt.Errorf("loading config: %w", err)

// Don't wrap if you don't want callers to inspect the inner error:
err = fmt.Errorf("loading config: %v", err)
```

---

## 2. log/slog — Structured Logging (Go 1.21+)

The `log` package is the old default logger (unstructured, goes to stderr). Use `slog` for structured, leveled logging.

```go
import "log/slog"

// Default logger (text format, stderr)
slog.Info("server started", "port", 8080)
slog.Warn("high memory usage", "percent", 92)
slog.Error("database error", "err", err, "query", query)
// Output: 2024/01/15 10:30:00 INFO server started port=8080

// JSON logger:
logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
logger.Info("server started", "port", 8080)
// Output: {"time":"2024-01-15T10:30:00Z","level":"INFO","msg":"server started","port":8080}

// With options (set minimum level):
opts := &slog.HandlerOptions{Level: slog.LevelDebug}
logger := slog.New(slog.NewJSONHandler(os.Stdout, opts))

slog.SetDefault(logger) // set as default, then slog.Info() uses it
```

### Log Levels

```go
slog.Debug("debug message", "detail", "verbose info")  // level -4
slog.Info("info message")                               // level 0
slog.Warn("warning message", "threshold", 100)          // level 4
slog.Error("error occurred", "err", err)                // level 8
```

### Structured Attributes

```go
// Key-value pairs (variadic):
slog.Info("request", "method", "GET", "path", "/api/users", "status", 200)

// Typed attributes (more efficient — avoids reflection):
slog.Info("request",
    slog.String("method", "GET"),
    slog.String("path", "/api/users"),
    slog.Int("status", 200),
    slog.Duration("latency", 42*time.Millisecond),
)

// Logger with permanent context fields:
reqLogger := logger.With("requestID", requestID, "userID", userID)
reqLogger.Info("processing request")
reqLogger.Info("request complete", "status", 200)
// Both log lines include requestID and userID
```

### Custom Handler

```go
type RequestHandler struct {
    slog.Handler
    requestID string
}

func (h *RequestHandler) Handle(ctx context.Context, r slog.Record) error {
    r.AddAttrs(slog.String("requestID", h.requestID))
    return h.Handler.Handle(ctx, r)
}
```

---

## 3. os & io — Files and Streams

### Reading Files

```go
import (
    "bufio"
    "io"
    "os"
)

// Read entire file into memory:
data, err := os.ReadFile("config.json")
if err != nil {
    return fmt.Errorf("reading config: %w", err)
}
// data is []byte

// Open file, read line by line (memory-efficient for large files):
f, err := os.Open("big.log")
if err != nil {
    return err
}
defer f.Close()

scanner := bufio.NewScanner(f)
for scanner.Scan() {
    line := scanner.Text()
    // process line
}
if err := scanner.Err(); err != nil {
    return fmt.Errorf("scanning file: %w", err)
}

// Read into buffer manually:
f, _ := os.Open("data.bin")
defer f.Close()
buf := make([]byte, 4096)
for {
    n, err := f.Read(buf)
    if err == io.EOF {
        break
    }
    if err != nil {
        return err
    }
    process(buf[:n])
}
```

### Writing Files

```go
// Write entire file at once:
err := os.WriteFile("output.txt", []byte("hello\n"), 0644)

// Create/truncate + write with bufio (efficient):
f, err := os.Create("output.txt") // creates or truncates
if err != nil {
    return err
}
defer f.Close()

w := bufio.NewWriter(f)
fmt.Fprintln(w, "line 1")
fmt.Fprintln(w, "line 2")
if err := w.Flush(); err != nil {  // MUST flush or buffered data is lost
    return err
}

// Append to file:
f, err := os.OpenFile("app.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
```

### os.Args and Environment Variables

```go
// Command line arguments
args := os.Args         // []string, args[0] is the program name
args[1:]                // everything after the program name

// For proper flag parsing, use the flag package or cobra:
import "flag"
port := flag.Int("port", 8080, "server port")
verbose := flag.Bool("verbose", false, "verbose output")
flag.Parse()
fmt.Println(*port, *verbose)

// Environment variables
home := os.Getenv("HOME")               // "" if not set
path, ok := os.LookupEnv("PATH")        // ok=false if not set
os.Setenv("MY_VAR", "value")

// Get all env vars:
for _, env := range os.Environ() {
    // "KEY=VALUE" format
    parts := strings.SplitN(env, "=", 2)
    fmt.Printf("%s = %s\n", parts[0], parts[1])
}
```

### io.Reader / io.Writer — The Core Interfaces

```go
type Reader interface { Read(p []byte) (n int, err error) }
type Writer interface { Write(p []byte) (n int, err error) }
```

Functions that accept `io.Reader` or `io.Writer` work with files, network connections, HTTP bodies, byte buffers, strings — anything:

```go
// io.Copy: copy from reader to writer
func copyFile(dst, src string) error {
    in, err := os.Open(src)
    if err != nil {
        return err
    }
    defer in.Close()

    out, err := os.Create(dst)
    if err != nil {
        return err
    }
    defer out.Close()

    _, err = io.Copy(out, in) // works for any reader/writer
    return err
}

// Adapter: wrap a string as a Reader
r := strings.NewReader("hello world")
io.Copy(os.Stdout, r)

// Adapter: wrap a []byte as a Reader
r := bytes.NewReader(data)

// Capture output into a buffer:
var buf bytes.Buffer
fmt.Fprintf(&buf, "count: %d", 42)
s := buf.String() // "count: 42"

// Read all from a reader:
data, err := io.ReadAll(resp.Body)

// Limit how much is read (prevent runaway requests):
limited := io.LimitReader(resp.Body, 1<<20) // max 1MB
data, err := io.ReadAll(limited)
```

---

## 4. encoding/json

### Marshal and Unmarshal

```go
type User struct {
    ID        int       `json:"id"`
    FirstName string    `json:"first_name"`
    LastName  string    `json:"last_name"`
    Password  string    `json:"-"`             // never include in JSON
    Score     float64   `json:"score,omitempty"` // omit if 0.0
    Tags      []string  `json:"tags,omitempty"`  // omit if nil/empty
    CreatedAt time.Time `json:"created_at"`
}

// Struct → JSON bytes
user := User{ID: 1, FirstName: "Alice", LastName: "Smith"}
data, err := json.Marshal(user)
if err != nil {
    return fmt.Errorf("marshaling user: %w", err)
}
fmt.Println(string(data))
// {"id":1,"first_name":"Alice","last_name":"Smith","created_at":"0001-01-01T00:00:00Z"}

// Pretty print:
data, err = json.MarshalIndent(user, "", "  ")

// JSON bytes → Struct
var u User
if err := json.Unmarshal(data, &u); err != nil {
    return fmt.Errorf("unmarshaling user: %w", err)
}
```

### Streaming Encoder/Decoder

For large payloads or HTTP request/response bodies, use encoder/decoder directly on io.Writer/io.Reader:

```go
// Encode to writer (HTTP response, file, etc.):
func writeJSON(w http.ResponseWriter, v any) {
    w.Header().Set("Content-Type", "application/json")
    enc := json.NewEncoder(w)
    enc.SetIndent("", "  ") // optional pretty print
    if err := enc.Encode(v); err != nil {
        log.Printf("encoding response: %v", err)
    }
}

// Decode from reader (HTTP request body, file, etc.):
func decodeBody(r *http.Request, v any) error {
    defer r.Body.Close()
    dec := json.NewDecoder(r.Body)
    dec.DisallowUnknownFields() // return error on unexpected fields
    return dec.Decode(v)
}

// Stream multiple JSON values:
dec := json.NewDecoder(strings.NewReader(`{"id":1}{"id":2}{"id":3}`))
for dec.More() {
    var item struct{ ID int `json:"id"` }
    if err := dec.Decode(&item); err != nil {
        break
    }
    fmt.Println(item.ID)
}
```

### Struct Tags Deep Dive

```go
type Example struct {
    // "json:"-"" — always omit
    Secret string `json:"-"`

    // "omitempty" — omit if zero value (0, false, "", nil, empty slice/map)
    Count int `json:"count,omitempty"`

    // "string" — encode number as JSON string (useful for JS large int compatibility)
    BigID int64 `json:"big_id,string"`

    // No tag — field name used as-is (case matters for export)
    Notes string // JSON key: "Notes"
}
```

### Custom MarshalJSON / UnmarshalJSON

```go
type Duration time.Duration

// Custom marshaling: duration as "1h30m" string instead of nanoseconds
func (d Duration) MarshalJSON() ([]byte, error) {
    return json.Marshal(time.Duration(d).String())
}

func (d *Duration) UnmarshalJSON(data []byte) error {
    var s string
    if err := json.Unmarshal(data, &s); err != nil {
        return err
    }
    dur, err := time.ParseDuration(s)
    if err != nil {
        return err
    }
    *d = Duration(dur)
    return nil
}

type Config struct {
    Timeout Duration `json:"timeout"`
}
// Marshals as: {"timeout":"30s"}
// Unmarshals from: {"timeout":"30s"}
```

---

## 5. net/http — HTTP Client and Server

### HTTP Server

```go
package main

import (
    "encoding/json"
    "log"
    "net/http"
    "time"
)

func main() {
    mux := http.NewServeMux()

    mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        w.Write([]byte("ok"))
    })

    mux.HandleFunc("GET /users/{id}", getUser)  // Go 1.22 path params
    mux.HandleFunc("POST /users", createUser)

    server := &http.Server{
        Addr:         ":8080",
        Handler:      mux,
        ReadTimeout:  5 * time.Second,   // time to read request headers+body
        WriteTimeout: 10 * time.Second,  // time to write response
        IdleTimeout:  60 * time.Second,  // keep-alive timeout
    }

    log.Println("listening on :8080")
    if err := server.ListenAndServe(); err != http.ErrServerClosed {
        log.Fatal(err)
    }
}

func getUser(w http.ResponseWriter, r *http.Request) {
    id := r.PathValue("id")  // Go 1.22 path parameter extraction

    user := struct {
        ID   string `json:"id"`
        Name string `json:"name"`
    }{ID: id, Name: "Alice"}

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(user)
}

func createUser(w http.ResponseWriter, r *http.Request) {
    var req struct {
        Name string `json:"name"`
    }
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        http.Error(w, "invalid request body", http.StatusBadRequest)
        return
    }
    // create user...
    w.WriteHeader(http.StatusCreated)
}
```

### Middleware

Middleware is a function that wraps an `http.Handler`:

```go
// Middleware signature
type Middleware func(http.Handler) http.Handler

func logging(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        start := time.Now()
        next.ServeHTTP(w, r)
        log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
    })
}

func requireAuth(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        token := r.Header.Get("Authorization")
        if token == "" {
            http.Error(w, "unauthorized", http.StatusUnauthorized)
            return
        }
        next.ServeHTTP(w, r)
    })
}

// Chain middleware (inner-out execution order):
func chain(h http.Handler, middleware ...Middleware) http.Handler {
    for i := len(middleware) - 1; i >= 0; i-- {
        h = middleware[i](h)
    }
    return h
}

// Usage:
mux.Handle("/api/", chain(apiHandler, logging, requireAuth))
```

### HTTP Client

Never use `http.DefaultClient` in production — it has no timeouts:

```go
// Custom client with timeouts:
client := &http.Client{
    Timeout: 10 * time.Second,
    Transport: &http.Transport{
        DialContext: (&net.Dialer{
            Timeout:   3 * time.Second,
            KeepAlive: 30 * time.Second,
        }).DialContext,
        MaxIdleConns:        100,
        MaxIdleConnsPerHost: 10,
        IdleConnTimeout:     90 * time.Second,
    },
}

// GET request:
resp, err := client.Get("https://api.example.com/users")
if err != nil {
    return fmt.Errorf("GET users: %w", err)
}
defer resp.Body.Close()  // ALWAYS close the body

if resp.StatusCode != http.StatusOK {
    body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
    return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, body)
}

var users []User
if err := json.NewDecoder(resp.Body).Decode(&users); err != nil {
    return fmt.Errorf("decoding response: %w", err)
}

// POST request with JSON body:
body, err := json.Marshal(newUser)
if err != nil {
    return err
}
req, err := http.NewRequestWithContext(ctx, "POST",
    "https://api.example.com/users", bytes.NewReader(body))
if err != nil {
    return err
}
req.Header.Set("Content-Type", "application/json")
req.Header.Set("Authorization", "Bearer "+token)

resp, err := client.Do(req)
```

---

## 6. time — Time and Duration

### time.Time

```go
now := time.Now()                 // current local time
utc := time.Now().UTC()           // UTC time
unix := time.Unix(1705312200, 0)  // from Unix timestamp (seconds, nanoseconds)

// Components:
now.Year(), now.Month(), now.Day()
now.Hour(), now.Minute(), now.Second()
now.Weekday()       // time.Monday, time.Tuesday, etc.
now.Unix()          // seconds since epoch
now.UnixMilli()     // milliseconds since epoch
now.UnixNano()      // nanoseconds since epoch

// Comparison:
t1.Before(t2)
t1.After(t2)
t1.Equal(t2)

// Arithmetic:
tomorrow := now.Add(24 * time.Hour)
yesterday := now.Add(-24 * time.Hour)
diff := t2.Sub(t1)  // returns time.Duration
```

### The Reference Time — Formatting

Go uses a specific reference time: **Mon Jan 2 15:04:05 MST 2006** (or in UTC: **Mon Jan 2 15:04:05 UTC 2006**). Use this exact date-time to define your format layout. This is confusing at first — memorize it or use the constants.

```go
// Reference: Mon Jan 2 15:04:05 MST 2006
t := time.Now()

t.Format("2006-01-02")                    // 2024-01-15
t.Format("2006-01-02 15:04:05")           // 2024-01-15 10:30:00
t.Format("January 2, 2006")              // January 15, 2024
t.Format("Mon, 02 Jan 2006 15:04:05 MST") // RFC1123
t.Format(time.RFC3339)                    // 2024-01-15T10:30:00Z (use this for JSON/APIs)
t.Format(time.RFC3339Nano)                // with nanoseconds
t.Format("3:04 PM")                       // 10:30 AM

// Parse:
t, err := time.Parse("2006-01-02", "2024-01-15")
t, err := time.Parse(time.RFC3339, "2024-01-15T10:30:00Z")

// Parse in specific timezone:
loc, _ := time.LoadLocation("America/New_York")
t, err := time.ParseInLocation("2006-01-02 15:04:05", "2024-01-15 10:30:00", loc)
```

### Duration

```go
d := 2*time.Hour + 30*time.Minute + 15*time.Second
d.Hours()    // 2.504166...
d.Minutes()  // 150.25
d.Seconds()  // 9015.0
d.String()   // "2h30m15s"

d, err := time.ParseDuration("2h30m")
```

### Ticker and Timer

```go
// Ticker: fires repeatedly on an interval
ticker := time.NewTicker(1 * time.Second)
defer ticker.Stop() // ALWAYS stop to prevent goroutine leak

for {
    select {
    case t := <-ticker.C:
        fmt.Println("tick at", t)
    case <-done:
        return
    }
}

// Timer: fires once after a delay
timer := time.NewTimer(5 * time.Second)
defer timer.Stop()

select {
case <-timer.C:
    fmt.Println("timer fired")
case <-ctx.Done():
    fmt.Println("cancelled")
}

// time.After: convenience, but leaks if not drained (use NewTimer in loops)
select {
case result := <-workCh:
    process(result)
case <-time.After(5 * time.Second):
    return errors.New("timeout")
}

// Sleep:
time.Sleep(100 * time.Millisecond)
```

---

## 7. strings & strconv

### strings Package

```go
import "strings"

s := "  Hello, World!  "

// Basic operations:
strings.ToUpper(s)                    // "  HELLO, WORLD!  "
strings.ToLower(s)                    // "  hello, world!  "
strings.TrimSpace(s)                  // "Hello, World!"
strings.Trim(s, " !")                 // "Hello, World"
strings.TrimLeft("xxhello", "x")      // "hello"
strings.TrimRight("helloxx", "x")     // "hello"
strings.TrimPrefix("hello.go", "hello") // ".go"
strings.TrimSuffix("hello.go", ".go")   // "hello"

// Search:
strings.Contains("seafood", "foo")    // true
strings.HasPrefix("seafood", "sea")   // true
strings.HasSuffix("seafood", "food")  // true
strings.Count("cheese", "e")          // 3
strings.Index("chicken", "ken")       // 4 (-1 if not found)
strings.LastIndex("go gopher", "go")  // 3

// Split and Join:
strings.Split("a,b,c", ",")           // ["a", "b", "c"]
strings.SplitN("a,b,c", ",", 2)       // ["a", "b,c"] — max 2 parts
strings.Fields("  foo bar  baz  ")    // ["foo", "bar", "baz"] — splits on whitespace
strings.Join([]string{"a","b","c"}, ", ") // "a, b, c"

// Replace:
strings.Replace("oink oink oink", "oink", "moo", 2) // "moo moo oink"
strings.ReplaceAll("oink oink oink", "oink", "moo")  // "moo moo moo"

// Repeat:
strings.Repeat("na", 4) // "nananana"
```

### strings.Builder (Efficient Concatenation)

```go
var sb strings.Builder
sb.WriteString("Hello")
sb.WriteRune(',')
sb.WriteByte(' ')
fmt.Fprintf(&sb, "World%s", "!")
result := sb.String() // "Hello, World!"
sb.Reset()            // reuse without allocation
```

### strconv Package

```go
import "strconv"

// int ↔ string
s := strconv.Itoa(42)          // "42"
n, err := strconv.Atoi("42")   // 42, nil

// More general parsing:
n64, err := strconv.ParseInt("42", 10, 64)    // base 10, 64-bit
n64, err := strconv.ParseInt("0xFF", 0, 64)   // base 0 = auto-detect
u64, err := strconv.ParseUint("42", 10, 64)
f64, err := strconv.ParseFloat("3.14", 64)
b, err := strconv.ParseBool("true")           // "1","t","T","TRUE","true" → true

// Format:
s = strconv.FormatInt(42, 16)   // "2a" (hex)
s = strconv.FormatFloat(3.14159, 'f', 2, 64) // "3.14"
s = strconv.FormatBool(true)    // "true"

// Quote/Unquote strings:
strconv.Quote(`Hello, "World"!`)      // `"Hello, \"World\"!"`
strconv.Unquote(`"hello\\nworld"`)    // "hello\nworld", nil
```

---

## 8. filepath & path

`path/filepath` handles OS-specific paths (uses `\` on Windows, `/` on Unix). `path` is for URL paths (always `/`).

```go
import "path/filepath"

// Join — handles separator and cleans the path:
filepath.Join("dir", "subdir", "file.txt") // "dir/subdir/file.txt" (Unix)
filepath.Join("/usr", "local", "../bin")    // "/usr/bin" (cleans ..)

// Split a path:
filepath.Dir("/usr/local/bin/go")   // "/usr/local/bin"
filepath.Base("/usr/local/bin/go")  // "go"
filepath.Ext("index.html")          // ".html"

dir, file := filepath.Split("/usr/local/bin/go")
// dir="usr/local/bin/", file="go"

// Absolute path:
abs, err := filepath.Abs("relative/path")
// relative to current working directory

// Clean (resolve . and ..):
filepath.Clean("/usr/./local/../bin") // "/usr/bin"

// Glob — pattern matching:
matches, err := filepath.Glob("*.go")           // all .go in current dir
matches, err = filepath.Glob("**/*.go")         // NOTE: does NOT recurse, use Walk

// Walk — recursive directory traversal:
err := filepath.Walk("/some/dir", func(path string, info os.FileInfo, err error) error {
    if err != nil {
        return err // propagate errors
    }
    if info.IsDir() {
        fmt.Println("dir:", path)
        return nil
    }
    fmt.Println("file:", path, info.Size(), "bytes")
    return nil
})

// WalkDir (Go 1.16+) — more efficient, uses fs.DirEntry instead of os.FileInfo:
err = filepath.WalkDir("/some/dir", func(path string, d fs.DirEntry, err error) error {
    if err != nil {
        return err
    }
    if !d.IsDir() && filepath.Ext(path) == ".go" {
        fmt.Println(path)
    }
    return nil
})
```

---

## 9. testing — Tests, Benchmarks, Examples

### Table-Driven Tests

The idiomatic Go testing pattern:

```go
package math_test

import (
    "testing"
    "your/module/math"
)

func TestAdd(t *testing.T) {
    tests := []struct {
        name string
        a, b int
        want int
    }{
        {"positive", 2, 3, 5},
        {"negative", -1, -2, -3},
        {"zero", 0, 0, 0},
        {"mixed", -1, 1, 0},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got := math.Add(tt.a, tt.b)
            if got != tt.want {
                t.Errorf("Add(%d, %d) = %d, want %d", tt.a, tt.b, got, tt.want)
            }
        })
    }
}
```

Run specific tests:
```bash
go test ./...                    # all tests
go test -run TestAdd ./...       # tests matching pattern
go test -run TestAdd/positive    # specific subtest
go test -v ./...                 # verbose output
```

### Parallel Tests

```go
func TestParallel(t *testing.T) {
    tests := []struct{ name, input, want string }{
        {"case1", "hello", "HELLO"},
        {"case2", "world", "WORLD"},
    }

    for _, tt := range tests {
        tt := tt  // capture range variable (needed pre-Go1.22)
        t.Run(tt.name, func(t *testing.T) {
            t.Parallel() // run this subtest in parallel with others
            got := strings.ToUpper(tt.input)
            if got != tt.want {
                t.Errorf("got %q, want %q", got, tt.want)
            }
        })
    }
}
```

### Testing Helpers

```go
func TestWithHelper(t *testing.T) {
    t.Helper() // marks this function as a test helper for cleaner error output
    // ...
}

// Setup and teardown with t.Cleanup:
func TestWithDB(t *testing.T) {
    db := setupTestDB(t)
    t.Cleanup(func() {
        db.Close()  // runs when test completes (including on failure)
    })
    // use db...
}

// Skip tests:
func TestOnLinux(t *testing.T) {
    if runtime.GOOS != "linux" {
        t.Skip("linux only")
    }
    // ...
}

// t.Fatal vs t.Error:
// t.Fatal: logs + stops the test immediately (like t.Error + t.FailNow)
// t.Error: logs + marks failed but continues running
if got != want {
    t.Fatalf("critical check failed: got %v, want %v", got, want)
}
```

### Benchmarks

```go
func BenchmarkAdd(b *testing.B) {
    x, y := 100, 200
    b.ResetTimer() // reset after any setup
    for i := 0; i < b.N; i++ {
        _ = math.Add(x, y)
    }
}

// Benchmark with sub-benchmarks:
func BenchmarkJSON(b *testing.B) {
    data := generateLargeStruct()

    b.Run("marshal", func(b *testing.B) {
        for i := 0; i < b.N; i++ {
            json.Marshal(data)
        }
    })

    b.Run("unmarshal", func(b *testing.B) {
        raw, _ := json.Marshal(data)
        var out MyStruct
        for i := 0; i < b.N; i++ {
            json.Unmarshal(raw, &out)
        }
    })
}
```

```bash
go test -bench=. ./...             # run all benchmarks
go test -bench=BenchmarkJSON -benchmem ./...  # with memory allocations
go test -bench=. -count=5 ./...   # run 5 times for stable results
```

### testdata Convention

Put test fixtures in a `testdata/` directory inside your package directory. Go tools ignore these files:

```
mypackage/
├── mypackage.go
├── mypackage_test.go
└── testdata/
    ├── input.json
    └── expected_output.json
```

```go
func TestFromFile(t *testing.T) {
    input, err := os.ReadFile("testdata/input.json")
    if err != nil {
        t.Fatal(err)
    }
    // use input...
}
```

---

## 10. Useful Third-Party Libraries

### testify/assert and testify/require

The most used testing helper library:

```bash
go get github.com/stretchr/testify
```

```go
import (
    "testing"
    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

func TestUser(t *testing.T) {
    user, err := createUser("Alice")

    // require: like t.Fatal — stops test immediately on failure
    require.NoError(t, err)
    require.NotNil(t, user)

    // assert: like t.Error — continues on failure
    assert.Equal(t, "Alice", user.Name)
    assert.Greater(t, user.ID, 0)
    assert.True(t, user.Active)
    assert.Len(t, user.Tags, 3)
    assert.Contains(t, user.Tags, "admin")
    assert.ErrorIs(t, someErr, ErrNotFound)
    assert.ElementsMatch(t, []int{3,1,2}, []int{1,2,3}) // order-independent
}

// Mock HTTP:
// github.com/stretchr/testify/mock for interface mocks
```

### spf13/cobra — CLI Applications

The standard for Go CLI tools (used by kubectl, docker, hugo, etc.):

```bash
go get github.com/spf13/cobra
```

```go
package main

import (
    "fmt"
    "github.com/spf13/cobra"
    "os"
)

func main() {
    var port int
    var verbose bool

    rootCmd := &cobra.Command{
        Use:   "myapp",
        Short: "My application",
        Long:  "A longer description of my application",
    }

    serveCmd := &cobra.Command{
        Use:   "serve",
        Short: "Start the HTTP server",
        RunE: func(cmd *cobra.Command, args []string) error {
            fmt.Printf("Starting server on port %d (verbose: %v)\n", port, verbose)
            return startServer(port)
        },
    }

    serveCmd.Flags().IntVarP(&port, "port", "p", 8080, "port to listen on")
    serveCmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "verbose output")

    versionCmd := &cobra.Command{
        Use:   "version",
        Short: "Print version",
        Run: func(cmd *cobra.Command, args []string) {
            fmt.Println("v1.0.0")
        },
    }

    rootCmd.AddCommand(serveCmd, versionCmd)

    if err := rootCmd.Execute(); err != nil {
        os.Exit(1)
    }
}
```

### samber/lo — Generic Slice/Map Utilities (Go 1.18+)

```bash
go get github.com/samber/lo
```

```go
import "github.com/samber/lo"

// Filter, Map, Reduce over slices:
nums := []int{1, 2, 3, 4, 5, 6}

evens := lo.Filter(nums, func(n, _ int) bool { return n%2 == 0 })
// [2, 4, 6]

doubled := lo.Map(nums, func(n, _ int) int { return n * 2 })
// [2, 4, 6, 8, 10, 12]

sum := lo.Reduce(nums, func(acc, n, _ int) int { return acc + n }, 0)
// 21

// Find:
val, found := lo.Find(nums, func(n int) bool { return n > 3 })
// 4, true

// Unique:
lo.Uniq([]int{1, 1, 2, 2, 3})
// [1, 2, 3]

// GroupBy:
words := []string{"apple", "ant", "bear", "bee", "cat"}
grouped := lo.GroupBy(words, func(w string) byte { return w[0] })
// map[97:[apple ant] 98:[bear bee] 99:[cat]]

// Keys/Values from map:
m := map[string]int{"a": 1, "b": 2}
lo.Keys(m)   // ["a", "b"]
lo.Values(m) // [1, 2]

// Chunk slice:
lo.Chunk([]int{1, 2, 3, 4, 5}, 2)
// [[1 2] [3 4] [5]]

// Contains:
lo.Contains([]string{"a", "b", "c"}, "b") // true

// Ternary (avoid verbose if-else):
label := lo.Ternary(isAdmin, "admin", "user")
```

---

## Quick Reference

```go
// Reading a file completely:
data, err := os.ReadFile("path")

// Writing a file completely:
err = os.WriteFile("path", data, 0644)

// JSON encode to HTTP response:
json.NewEncoder(w).Encode(v)

// JSON decode from HTTP request:
json.NewDecoder(r.Body).Decode(&v)

// HTTP client with timeout:
client := &http.Client{Timeout: 10 * time.Second}
resp, err := client.Get(url)
defer resp.Body.Close()

// Current time in RFC3339:
time.Now().UTC().Format(time.RFC3339)

// String to int:
n, err := strconv.Atoi(s)

// Int to string:
s := strconv.Itoa(n)

// Build a string efficiently:
var sb strings.Builder
sb.WriteString("hello")
result := sb.String()

// Walk directory:
filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error { ... })

// Table-driven test skeleton:
tests := []struct{ name, input, want string }{...}
for _, tt := range tests {
    t.Run(tt.name, func(t *testing.T) { ... })
}
```
