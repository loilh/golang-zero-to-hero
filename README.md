# Go: Zero to Hero

A practical Go tutorial for developers who already know how to program. If you know Python, JavaScript, Java, Rust, or any other language, this guide skips the basics and focuses on what makes Go *different* — and why those differences matter.

---

## What Makes Go Different

### Compiled, but Fast to Compile

Go compiles to a single native binary with no runtime dependency, no JVM, no interpreter. A 100k-line project compiles in seconds. The binary runs on the target machine without installing anything. This is not a minor point — it fundamentally changes how you deploy software.

### Statically Typed with Inference

Every value has a type known at compile time. But Go's `:=` operator infers types, so you rarely write them explicitly. You get the safety of static typing with much of the convenience of dynamic typing.

### Garbage Collected

Unlike C/C++ or Rust, you do not manage memory manually. Unlike Java/Python, the GC is tuned for low latency (sub-millisecond pause times), not throughput. You can write servers that handle millions of requests without GC pauses killing your p99 latency.

### Built-in Concurrency — Not Bolted On

Goroutines and channels are first-class language features. Spawning a concurrent task is `go fn()`. Communication between goroutines is `chan`. The runtime multiplexes thousands of goroutines onto a small thread pool. This is Go's defining feature.

### No Exceptions

Go uses explicit error return values instead of exceptions. Functions return `(result, error)`. The caller decides what to do with errors. There is no hidden control flow. This is controversial — but it makes code easier to reason about and forces you to think about failure paths at every step.

### No Inheritance

Go has no class hierarchy, no `extends`, no `super`. Instead it uses **composition via embedding** and **interfaces via implicit implementation**. An interface is satisfied by any type that has the required methods — no declaration needed. This leads to very decoupled code.

### Opinionated Formatting

`gofmt` formats your code. There are no style debates. All Go code in the world looks the same. This sounds minor; it is not.

---

## Prerequisites

**Install Go 1.22 or later:**

```bash
# macOS
brew install go

# Linux
wget https://go.dev/dl/go1.22.0.linux-amd64.tar.gz
sudo tar -C /usr/local -xzf go1.22.0.linux-amd64.tar.gz
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.profile

# Verify
go version
# go version go1.22.0 linux/amd64
```

**Editor Setup:**

VS Code with the [Go extension](https://marketplace.visualstudio.com/items?itemName=golang.Go) gives you:
- `gopls` (Go language server) — autocomplete, go-to-definition, find references
- `dlv` (Delve debugger) — breakpoints, step-through debugging
- `staticcheck` — linter beyond `go vet`

Install tools after the extension prompts you, or run:
```bash
go install golang.org/x/tools/gopls@latest
go install github.com/go-delve/delve/cmd/dlv@latest
```

**GoLand** (JetBrains) is also excellent if you prefer a full IDE.

---

## Quick Start: Hello World

```bash
# Create a new project
mkdir hello && cd hello

# Initialize a module (use your real module path in real projects)
go mod init example.com/hello
# Creates go.mod
```

`main.go`:
```go
package main

import "fmt"

func main() {
    fmt.Println("Hello, World!")
}
```

```bash
# Run directly (compile + execute in one step)
go run main.go

# Build a binary
go build -o hello .
./hello

# Build for a different platform (cross-compilation, built in)
GOOS=linux GOARCH=amd64 go build -o hello-linux .
GOOS=windows GOARCH=amd64 go build -o hello.exe .
```

`go.mod` after `go mod init`:
```
module example.com/hello

go 1.22
```

After adding external dependencies (`go get github.com/some/pkg`), a `go.sum` file appears with cryptographic checksums of every dependency. Commit both files.

---

## Table of Contents

| Chapter | Topic | File |
|---------|-------|------|
| 01 | [Fundamentals](./01-fundamentals/notes.md) | Packages, types, functions, errors, pointers, interfaces, defer |
| 02 | [Concurrency](./02-concurrency/notes.md) | Goroutines, channels, select, sync, context, patterns |
| 03 | [Standard Library](./03-standard-library/notes.md) | fmt, slog, os/io, JSON, HTTP, time, testing |

---

## How to Use This Tutorial

Each chapter is a self-contained `notes.md` with explanations and runnable code. To run the examples:

1. Create a directory with `go mod init example.com/learn`
2. Copy the example into a `.go` file
3. Run with `go run filename.go`

Examples are complete and runnable unless marked `// fragment`.

When you see this pattern in code comments:
- `// Good` — idiomatic Go
- `// Bad` / `// Avoid` — common mistake or anti-pattern
- `// Output:` — expected output when you run the code

---

## Go Toolchain Cheat Sheet

```bash
go mod init <module-path>   # Start a new module
go mod tidy                 # Add missing, remove unused dependencies
go get github.com/pkg@v1.2  # Add or upgrade a dependency
go build ./...              # Build all packages
go test ./...               # Run all tests
go test -race ./...         # Run tests with race detector
go vet ./...                # Static analysis
go fmt ./...                # Format all files (or use gofmt)
go doc fmt.Println          # Show documentation
go list -m all              # List all dependencies
go clean -modcache          # Clear module cache
```
