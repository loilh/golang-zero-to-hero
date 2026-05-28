# Chapter 01 — Fundamentals

For developers coming from Python, JavaScript, Java, or similar languages. This chapter focuses on concepts that are *different* in Go, not concepts that are universal to all languages.

---

## 1. Packages & Modules

### Module vs Package

A **module** is a collection of packages with a `go.mod` file at the root. A **package** is a directory of `.go` files that share the same `package` declaration. One module, many packages.

```
myapp/                    ← module root (contains go.mod)
├── go.mod
├── main.go               ← package main
├── config/
│   └── config.go         ← package config
├── handlers/
│   ├── user.go           ← package handlers
│   └── product.go        ← package handlers (same dir = same package)
└── internal/
    └── db/
        └── db.go         ← package db (only importable within this module)
```

`go.mod`:
```
module github.com/myorg/myapp

go 1.22

require (
    github.com/lib/pq v1.10.9
)
```

### Import Paths

```go
package main

import (
    "fmt"                              // stdlib
    "os"                               // stdlib
    "github.com/myorg/myapp/config"    // your own package
    "github.com/lib/pq"                // external dependency

    // Import with alias (avoids naming conflicts)
    myfmt "github.com/myorg/myapp/fmt"

    // Blank import (side effects only — runs init())
    _ "github.com/lib/pq"
)
```

Package names are the last segment of the import path by convention. `import "github.com/lib/pq"` means you use it as `pq.Something`. If a package name differs from its directory, it's declared explicitly: `package pgdriver` in a dir called `pq`. Check with `go doc`.

### Package Visibility

Go has two visibility levels — no `public`, `protected`, `private` keywords:

- **Exported** (public): identifier starts with an uppercase letter — `MyFunc`, `Config`, `MaxRetries`
- **Unexported** (private to the package): lowercase — `myFunc`, `config`, `maxRetries`

```go
package config

// Exported — usable from other packages
type Config struct {
    Host string
    Port int
    dsn  string  // unexported field — not accessible outside config package
}

// Exported function
func New(host string, port int) *Config {
    return &Config{Host: host, Port: port, dsn: buildDSN(host, port)}
}

// unexported function — internal helper
func buildDSN(host string, port int) string {
    return fmt.Sprintf("%s:%d", host, port)
}
```

### init() Functions

`init()` runs automatically after all variable declarations in the package, before `main()`. A package can have multiple `init()` functions, even in the same file. You cannot call `init()` explicitly.

```go
package db

import (
    "database/sql"
    "log"
    _ "github.com/lib/pq" // blank import triggers pq's init(), registering the driver
)

var db *sql.DB

func init() {
    var err error
    db, err = sql.Open("postgres", "postgres://localhost/mydb?sslmode=disable")
    if err != nil {
        log.Fatal("failed to connect to database:", err)
    }
}
```

Use `init()` sparingly — it makes execution order harder to follow and can hide errors. Prefer explicit initialization functions that return errors.

---

## 2. Type System

### Strong Static Typing

Go does not do implicit type conversion. Unlike C or JavaScript, you must convert explicitly:

```go
var x int = 42
var y float64 = float64(x)  // explicit conversion required
var z int = int(y)           // explicit conversion, truncates decimal

// This does NOT compile:
// var bad float64 = x  // cannot use x (int) as float64
```

### Zero Values

Every variable in Go has a well-defined zero value. No undefined, no null-by-accident:

```go
var i int        // 0
var f float64    // 0.0
var b bool       // false
var s string     // "" (empty string)
var p *int       // nil
var sl []int     // nil (but len(sl) == 0, safe to append)
var m map[string]int  // nil (reading is safe, writing panics!)
var ch chan int   // nil (sending/receiving blocks forever)

// Structs: each field gets its zero value
type Point struct{ X, Y int }
var pt Point  // Point{X: 0, Y: 0}
```

### Type Inference with :=

`:=` is the short variable declaration. It infers the type from the right-hand side. Only works inside function bodies.

```go
// These are equivalent:
var name string = "Alice"
var name = "Alice"      // type inferred
name := "Alice"         // short declaration, most common inside functions

// Multiple assignment
x, y := 10, 20
x, y = y, x  // swap — no temp variable needed

// := can redeclare if at least one variable is new
val, err := someFunc()
val, err = otherFunc()  // = not :=, both already declared
newVal, err := thirdFunc()  // OK: newVal is new, err is redeclared
```

### Named Types and Type Aliases

```go
// Named type — creates a NEW type, even though underlying type is int
type UserID int
type ProductID int

var uid UserID = 42
var pid ProductID = 42

// This does NOT compile — different types:
// uid = pid

// Explicit conversion required:
uid = UserID(pid)

// Type alias — just another name for the SAME type
type MyString = string  // identical to string, no conversion needed
```

Named types are very useful for domain modeling and attaching methods:

```go
type Celsius float64
type Fahrenheit float64

func (c Celsius) ToFahrenheit() Fahrenheit {
    return Fahrenheit(c*9/5 + 32)
}

func main() {
    temp := Celsius(100)
    fmt.Println(temp.ToFahrenheit()) // 212
}
```

---

## 3. Built-in Types

### Integer Types

| Type | Size | Range |
|------|------|-------|
| `int` | platform (32 or 64 bit) | matches pointer size |
| `int8` | 8 bit | -128 to 127 |
| `int16` | 16 bit | -32768 to 32767 |
| `int32` | 32 bit | -2^31 to 2^31-1 |
| `int64` | 64 bit | -2^63 to 2^63-1 |
| `uint`, `uint8`, `uint16`, `uint32`, `uint64` | same sizes, unsigned | |
| `byte` | alias for `uint8` | used for raw bytes |
| `rune` | alias for `int32` | represents a Unicode code point |
| `uintptr` | platform | holds a pointer value, for unsafe code |

Use `int` for most integer work. Use specific sizes when interacting with binary protocols, files, or C code.

### Strings, Bytes, and Runes

Strings in Go are **immutable sequences of bytes** (not characters). They are UTF-8 by convention but not enforced. Indexing a string gives a `byte`, not a character:

```go
s := "Hello, 世界"

fmt.Println(len(s))     // 13 — byte length, NOT character count
fmt.Println(s[7])       // 228 — first byte of '世', not '世'

// To iterate over characters (runes), use range:
for i, r := range s {
    fmt.Printf("index %d: %c (U+%04X)\n", i, r, r)
}
// index 0: H (U+0048)
// index 7: 世 (U+4E16)   ← byte index, not character index
// index 10: 界 (U+754C)

// Character count:
import "unicode/utf8"
fmt.Println(utf8.RuneCountInString(s)) // 9

// String ↔ []byte conversion (copies the data):
b := []byte(s)
s2 := string(b)

// String ↔ []rune conversion:
runes := []rune(s)
fmt.Println(len(runes)) // 9
```

String concatenation with `+` in a loop is O(n²). Use `strings.Builder`:

```go
var sb strings.Builder
for _, word := range words {
    sb.WriteString(word)
    sb.WriteByte(' ')
}
result := sb.String()
```

### Arrays vs Slices

Arrays have a fixed size that is part of their type. `[3]int` and `[4]int` are different types. Arrays are values — assigning copies the entire array.

```go
// Arrays — rarely used directly
a := [3]int{1, 2, 3}
b := a        // b is a COPY
b[0] = 99
fmt.Println(a[0]) // 1 — a is unchanged

// Slices — the workhorse
s := []int{1, 2, 3}
t := s        // t references the SAME underlying array
t[0] = 99
fmt.Println(s[0]) // 99 — s is changed!
```

**Slice internals**: a slice header is 3 words — pointer to backing array, length, capacity.

```go
s := make([]int, 3, 5)  // len=3, cap=5
fmt.Println(len(s), cap(s)) // 3 5

s = append(s, 4, 5)     // len=5, cap=5 — no reallocation
s = append(s, 6)         // len=6, cap=10 — new backing array allocated (cap doubles)

// Always use the return value of append — it may return a new slice
s = append(s, newElement)  // Good
append(s, newElement)       // Bad — return value discarded, likely a bug

// Slicing a slice — shares backing array
a := []int{0, 1, 2, 3, 4}
b := a[1:3]  // [1, 2], len=2, cap=4 (from index 1 to end of a)
b[0] = 99
fmt.Println(a) // [0 99 2 3 4] — shared!

// Full slice expression limits capacity to prevent sharing:
c := a[1:3:3]  // len=2, cap=2 — append to c won't touch a
```

Copying slices:

```go
src := []int{1, 2, 3}
dst := make([]int, len(src))
n := copy(dst, src)  // copies min(len(dst), len(src)) elements, returns count
```

### Maps

```go
// Create
m := map[string]int{}           // empty map literal — ready to use
m := make(map[string]int)       // equivalent
m := make(map[string]int, 100)  // hint initial capacity (optimization)

// A nil map is readable (returns zero values) but not writable:
var m map[string]int  // nil map
_ = m["key"]          // OK, returns 0
m["key"] = 1          // PANIC: assignment to entry in nil map

// Read with existence check:
val, ok := m["key"]
if !ok {
    // key not present
}

// Delete:
delete(m, "key")

// Iterate (order is RANDOM — intentionally randomized):
for k, v := range m {
    fmt.Printf("%s: %d\n", k, v)
}
```

Maps are **not safe for concurrent use** — use `sync.Map` or a `sync.RWMutex` around a regular map.

### Struct Tags

Struct tags are metadata attached to fields, used by encoding packages, ORMs, validation libraries, etc.:

```go
type User struct {
    ID        int    `json:"id" db:"user_id"`
    FirstName string `json:"first_name" db:"first_name"`
    Password  string `json:"-"`               // omit from JSON entirely
    Age       int    `json:"age,omitempty"`   // omit if zero value
    Score     float64 `json:"score,string"`  // encode number as JSON string
}
```

---

## 4. Functions

### Multiple Return Values

This is Go's answer to exceptions. Instead of throwing, functions return an error as the last return value.

```go
func divide(a, b float64) (float64, error) {
    if b == 0 {
        return 0, fmt.Errorf("division by zero")
    }
    return a / b, nil
}

result, err := divide(10, 2)
if err != nil {
    log.Fatal(err)
}
fmt.Println(result) // 5
```

### Named Return Values

Name the return values in the signature. They become variables in scope. A bare `return` returns them. Use sparingly — can hurt readability in long functions, but great for short ones.

```go
func minMax(nums []int) (min, max int) {
    if len(nums) == 0 {
        return // returns 0, 0 (zero values)
    }
    min, max = nums[0], nums[0]
    for _, n := range nums[1:] {
        if n < min {
            min = n
        }
        if n > max {
            max = n
        }
    }
    return // returns min, max
}
```

Named returns are also useful with `defer` to modify the return value:

```go
func readFile(path string) (content string, err error) {
    f, err := os.Open(path)
    if err != nil {
        return // named return: content="", err=the open error
    }
    defer func() {
        if closeErr := f.Close(); closeErr != nil && err == nil {
            err = closeErr // modify named return
        }
    }()
    // ... read ...
    return
}
```

### Variadic Functions

```go
func sum(nums ...int) int {
    total := 0
    for _, n := range nums {
        total += n
    }
    return total
}

sum(1, 2, 3)         // 6
sum(1, 2, 3, 4, 5)   // 15

// Spread a slice into a variadic call:
nums := []int{1, 2, 3}
sum(nums...)         // same as sum(1, 2, 3)
```

### First-Class Functions and Closures

Functions are values. Assign them, pass them, return them.

```go
// Function type
type Transformer func(string) string

// Higher-order function
func applyAll(s string, fns ...Transformer) string {
    for _, fn := range fns {
        s = fn(s)
    }
    return s
}

upper := strings.ToUpper
trim  := strings.TrimSpace

result := applyAll("  hello world  ", trim, upper)
fmt.Println(result) // "HELLO WORLD"

// Closure captures variables from outer scope
func makeCounter() func() int {
    count := 0
    return func() int {
        count++
        return count
    }
}

counter := makeCounter()
fmt.Println(counter()) // 1
fmt.Println(counter()) // 2
fmt.Println(counter()) // 3
```

**Gotcha — loop variable capture** (fixed in Go 1.22 with per-iteration variables, but know the old behavior):

```go
// Go 1.21 and earlier — all closures capture the SAME loop variable:
fns := make([]func(), 3)
for i := 0; i < 3; i++ {
    i := i  // shadow with a new variable (old workaround)
    fns[i] = func() { fmt.Println(i) }
}
// Go 1.22+: each iteration creates a new i, so shadowing is not needed
```

---

## 5. Error Handling

Go has no exceptions. Errors are values. This is one of the most debated Go design decisions, but it produces explicit, readable error handling.

### The error Interface

```go
// error is a built-in interface:
type error interface {
    Error() string
}

// nil means no error
var err error = nil
```

### errors.New and fmt.Errorf

```go
import (
    "errors"
    "fmt"
)

// Simple error with static message
var ErrNotFound = errors.New("not found")

// Formatted error
func findUser(id int) (*User, error) {
    if id <= 0 {
        return nil, fmt.Errorf("invalid user id: %d", id)
    }
    // ...
}

// Wrapping errors — %w verb wraps for errors.Is/As to unwrap
func getUser(id int) (*User, error) {
    user, err := db.QueryUser(id)
    if err != nil {
        return nil, fmt.Errorf("getUser(%d): %w", id, err)
    }
    return user, nil
}
```

### errors.Is and errors.As

```go
var ErrNotFound = errors.New("not found")

func findRecord(id int) error {
    return fmt.Errorf("findRecord: %w", ErrNotFound)  // wraps ErrNotFound
}

err := findRecord(42)

// errors.Is: checks if any error in the chain matches
if errors.Is(err, ErrNotFound) {
    fmt.Println("record not found")  // prints this
}

// errors.As: checks if any error in the chain is a specific type
type ValidationError struct {
    Field   string
    Message string
}

func (e *ValidationError) Error() string {
    return fmt.Sprintf("validation error: %s — %s", e.Field, e.Message)
}

func validate(name string) error {
    if name == "" {
        return fmt.Errorf("validate: %w", &ValidationError{Field: "name", Message: "required"})
    }
    return nil
}

err := validate("")
var ve *ValidationError
if errors.As(err, &ve) {
    fmt.Println(ve.Field, ve.Message) // name required
}
```

### Sentinel Errors

Package-level error variables that callers can compare against. Document them — they're part of your API.

```go
package store

import "errors"

// Sentinel errors — exported, comparable with errors.Is
var (
    ErrNotFound   = errors.New("store: record not found")
    ErrDuplicate  = errors.New("store: duplicate record")
    ErrPermission = errors.New("store: permission denied")
)
```

### Contrast with try/catch

```python
# Python
try:
    result = divide(a, b)
except ZeroDivisionError as e:
    handle(e)
```

```go
// Go — explicit at every call site
result, err := divide(a, b)
if err != nil {
    handle(err)
}
```

The Go approach is verbose but: every function that can fail is obvious from its signature, error handling is always visible, and there's no hidden propagation path to trace. Libraries like [github.com/pkg/errors](https://github.com/pkg/errors) and patterns like `must()` helpers reduce some boilerplate for programs (not libraries).

---

## 6. Pointers

Go has pointers. Unlike C, there is no pointer arithmetic. Unlike Java, primitives and structs can both be on the stack or heap.

```go
x := 42
p := &x        // p is *int, holds the address of x
fmt.Println(*p) // 42 — dereference
*p = 99
fmt.Println(x)  // 99 — x changed through pointer

var q *int     // nil pointer
fmt.Println(q) // <nil>
// *q = 1      // PANIC: nil pointer dereference
```

### Value Receivers vs Pointer Receivers

This is one of the most important decisions in Go struct design.

```go
type Counter struct {
    count int
}

// Value receiver — receives a COPY of Counter
// Cannot modify the original
func (c Counter) Value() int {
    return c.count
}

// Pointer receiver — receives a pointer to Counter
// Can modify the original, and avoids copying
func (c *Counter) Increment() {
    c.count++
}

func (c *Counter) Reset() {
    c.count = 0
}

c := Counter{}
c.Increment()           // Go auto-takes address: (&c).Increment()
fmt.Println(c.Value())  // 1
```

**Rules of thumb:**
- If the method modifies the receiver — use pointer receiver
- If the receiver is large (copying is expensive) — use pointer receiver
- If the receiver is a small value type (int, point, etc.) — value receiver is fine
- Be consistent: if any method has a pointer receiver, all methods should have pointer receivers

### When to Use Pointers

```go
// Use pointer when:
// 1. You want to modify the struct
func (u *User) UpdateEmail(email string) {
    u.Email = email
}

// 2. Struct is large
func process(data *LargeStruct) { ... }

// 3. Optional/nullable value (pointer can be nil)
type Config struct {
    Timeout *time.Duration  // nil means "use default"
}

// 4. Need shared mutable state
cache := &Cache{}
worker1(cache)
worker2(cache)

// Use value when:
// 1. Simple immutable data
type Point struct{ X, Y float64 }
func (p Point) Distance(other Point) float64 { ... }

// 2. Small struct where copying is cheap and you want isolation
```

---

## 7. Structs

### Definition and Literals

```go
type Rectangle struct {
    Width  float64
    Height float64
}

// Struct literal with field names (preferred — order independent, clear)
r1 := Rectangle{Width: 10.5, Height: 5.0}

// Positional (avoid — breaks if fields are reordered)
r2 := Rectangle{10.5, 5.0}

// Zero value + field assignment
var r3 Rectangle
r3.Width = 10.5
r3.Height = 5.0

// Pointer to struct
r4 := &Rectangle{Width: 10.5, Height: 5.0}
r4.Width = 20  // auto-dereferenced, same as (*r4).Width = 20
```

### Embedding — Composition Over Inheritance

Go has no inheritance. Use embedding to compose types:

```go
type Animal struct {
    Name string
}

func (a Animal) Speak() string {
    return a.Name + " makes a sound"
}

type Dog struct {
    Animal        // embedded — promotes Animal's fields and methods to Dog
    Breed string
}

func (d Dog) Speak() string {
    return d.Name + " barks"  // override
}

d := Dog{
    Animal: Animal{Name: "Rex"},
    Breed:  "Labrador",
}

fmt.Println(d.Name)   // Rex — promoted field
fmt.Println(d.Speak()) // Rex barks — Dog's method overrides Animal's
fmt.Println(d.Animal.Speak()) // Rex makes a sound — access embedded directly
```

Embedding an interface:

```go
type ReadWriter interface {
    io.Reader  // embedded interface
    io.Writer
}
```

### Anonymous Structs

```go
// Useful for one-off data grouping, test cases, JSON parsing
point := struct {
    X, Y int
}{X: 1, Y: 2}

// Common in table-driven tests
tests := []struct {
    input    string
    expected int
}{
    {"hello", 5},
    {"world!", 6},
    {"", 0},
}

for _, tt := range tests {
    got := len(tt.input)
    if got != tt.expected {
        t.Errorf("len(%q) = %d, want %d", tt.input, got, tt.expected)
    }
}
```

---

## 8. Interfaces

### Implicit Implementation

This is Go's most distinctive feature compared to Java/C#. You do **not** declare `implements`. If a type has all the methods of an interface, it satisfies that interface automatically.

```java
// Java — explicit
class Dog implements Animal {
    public String speak() { return "woof"; }
}
```

```go
// Go — implicit
type Speaker interface {
    Speak() string
}

type Dog struct{ Name string }

func (d Dog) Speak() string { return d.Name + " says woof" }

// Dog satisfies Speaker — no declaration needed
var s Speaker = Dog{Name: "Rex"}
fmt.Println(s.Speak())
```

This means you can satisfy interfaces from packages you don't own. The `io.Reader` interface is:
```go
type Reader interface {
    Read(p []byte) (n int, err error)
}
```
Any type with a `Read([]byte) (int, error)` method is automatically an `io.Reader`.

### Interface Design

Keep interfaces small. The most powerful interfaces in Go's stdlib have one or two methods:

```go
type Reader interface { Read(p []byte) (n int, err error) }
type Writer interface { Write(p []byte) (n int, err error) }
type Closer interface { Close() error }
type Stringer interface { String() string }
type Error interface { Error() string }
```

### interface{} and any

`interface{}` (aliased as `any` since Go 1.18) is the empty interface. Every type satisfies it. It's Go's equivalent of `Object` in Java, but more explicit.

```go
func printAnything(v any) {
    fmt.Printf("type: %T, value: %v\n", v, v)
}

printAnything(42)        // type: int, value: 42
printAnything("hello")   // type: string, value: hello
printAnything([]int{1,2}) // type: []int, value: [1 2]
```

Avoid `any` in APIs when you can use generics (Go 1.18+) or a concrete type.

### Type Assertions

Extract the concrete type from an interface:

```go
var i interface{} = "hello"

// Single-value: panics if wrong type
s := i.(string)
fmt.Println(s) // hello

// Two-value: safe, returns ok bool
s, ok := i.(string)
if ok {
    fmt.Println(s)
}
n, ok := i.(int)
fmt.Println(n, ok) // 0 false
```

### Type Switches

```go
func describe(i interface{}) string {
    switch v := i.(type) {
    case int:
        return fmt.Sprintf("int: %d", v)
    case string:
        return fmt.Sprintf("string of length %d: %q", len(v), v)
    case bool:
        return fmt.Sprintf("bool: %v", v)
    case []int:
        return fmt.Sprintf("[]int with %d elements", len(v))
    case nil:
        return "nil"
    default:
        return fmt.Sprintf("unknown type: %T", v)
    }
}
```

---

## 9. defer / panic / recover

### defer

`defer` schedules a function call to run when the enclosing function returns, regardless of how it returns (normal, error, panic). LIFO order — last deferred runs first.

```go
func processFile(path string) error {
    f, err := os.Open(path)
    if err != nil {
        return err
    }
    defer f.Close()  // runs when processFile returns, even on error

    // No need to remember to close f in every return path
    data, err := io.ReadAll(f)
    if err != nil {
        return err
    }
    return process(data)
}
```

defer arguments are evaluated immediately (at the defer statement), not when the deferred function runs:

```go
x := 10
defer fmt.Println(x) // captures x=10 now
x = 20
// prints 10, not 20
```

Multiple defers — LIFO:

```go
func main() {
    defer fmt.Println("first")
    defer fmt.Println("second")
    defer fmt.Println("third")
    // Output:
    // third
    // second
    // first
}
```

### panic and recover

`panic` terminates the goroutine, running all deferred functions in that goroutine's stack before crashing.

```go
func mustPositive(n int) int {
    if n <= 0 {
        panic(fmt.Sprintf("expected positive, got %d", n))
    }
    return n
}
```

`recover` catches a panic — **only from within a deferred function**:

```go
func safeDiv(a, b int) (result int, err error) {
    defer func() {
        if r := recover(); r != nil {
            err = fmt.Errorf("recovered from panic: %v", r)
        }
    }()
    result = a / b  // panics if b == 0
    return
}

r, err := safeDiv(10, 0)
fmt.Println(r, err) // 0 recovered from panic: runtime error: integer divide by zero
```

**When to use panic:**
- Programmer errors that should never happen (index out of range is automatic)
- `init()` failure that makes the program unusable
- `Must*` wrappers in package-level initialization

**Do not use panic for regular error conditions** — use the `(value, error)` pattern instead.

---

## 10. Constants and iota

### Typed vs Untyped Constants

```go
// Untyped constants — more flexible, no explicit type
const Pi = 3.14159265358979
const MaxConnections = 100

// Typed constants — explicit type
const MaxRetries int = 3
const AppName string = "myapp"
```

Untyped constants can be used with any compatible type without conversion:

```go
const Big = 1000000
var x int32 = Big    // OK
var y int64 = Big    // OK
var z float64 = Big  // OK
```

### iota — Enumeration

`iota` is a counter that starts at 0 and increments by 1 for each constant in a `const` block:

```go
type Weekday int

const (
    Sunday Weekday = iota  // 0
    Monday                  // 1
    Tuesday                 // 2
    Wednesday               // 3
    Thursday                // 4
    Friday                  // 5
    Saturday                // 6
)

fmt.Println(Monday) // 1
```

iota in expressions:

```go
type ByteSize float64

const (
    _           = iota             // skip zero with blank identifier
    KB ByteSize = 1 << (10 * iota) // 1 << 10 = 1024
    MB                             // 1 << 20 = 1048576
    GB                             // 1 << 30
    TB                             // 1 << 40
)

fmt.Println(KB, MB, GB) // 1024 1.048576e+06 1.073741824e+09
```

Bit flags:

```go
type Permission uint

const (
    Read    Permission = 1 << iota // 1
    Write                          // 2
    Execute                        // 4
)

perm := Read | Write
fmt.Println(perm & Read != 0)    // true — has read permission
fmt.Println(perm & Execute != 0) // false — no execute permission
```

Stringer for iota enums (implement `String()` so `fmt.Println` shows names):

```go
func (d Weekday) String() string {
    names := []string{"Sunday", "Monday", "Tuesday", "Wednesday",
        "Thursday", "Friday", "Saturday"}
    if d < Sunday || d > Saturday {
        return fmt.Sprintf("Weekday(%d)", int(d))
    }
    return names[d]
}

fmt.Println(Wednesday) // Wednesday
```

Or use `go generate` with `stringer` tool: `//go:generate stringer -type=Weekday`

---

## Common Mistakes Summary

| Mistake | Fix |
|---------|-----|
| Writing to a nil map | Initialize with `make(map[K]V)` or a map literal |
| Ignoring errors (`result, _ := fn()`) | Handle or explicitly document why you're ignoring |
| Forgetting `append` return value | Always `s = append(s, ...)` |
| Loop variable capture in closures | Use per-iteration variable (Go 1.22+) or shadow: `x := x` |
| Comparing structs with slices/maps | Use `reflect.DeepEqual` or a custom Equal method |
| Shadowing `err` with `:=` | Watch for `err` in nested scopes masking outer `err` |
| Pointer receiver on value type | Methods with pointer receivers can't be called on non-addressable values |
