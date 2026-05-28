# Chapter 7: Microservices in Go — E-Commerce Order Processing System

## Overview

We'll build a production-grade microservices system for e-commerce order processing. Three independent services communicate via gRPC and Kafka.

```
[Client] → HTTP/REST → [order-service :8080]
                              ↓ gRPC :50051
                        [user-service]
                              ↓ Kafka topic: order-events
                  [notification-service]
```

**Tech stack:**
- gRPC + Protocol Buffers for synchronous service-to-service calls
- Apache Kafka for async event streaming
- Docker Compose for local orchestration

---

## Project Structure

```
microservices/
├── proto/
│   └── user/
│       └── user.proto
├── user-service/
│   ├── main.go
│   ├── server.go
│   └── go.mod
├── order-service/
│   ├── main.go
│   ├── handler.go
│   ├── kafka_producer.go
│   └── go.mod
├── notification-service/
│   ├── main.go
│   ├── consumer.go
│   └── go.mod
└── docker-compose.yml
```

---

## Part 1: Protocol Buffers & gRPC Definition

### 1.1 Writing user.proto

```protobuf
// proto/user/user.proto
syntax = "proto3";

package user;

option go_package = "github.com/yourorg/microservices/proto/user";

// User message — the core domain object
message User {
  string id         = 1;
  string name       = 2;
  string email      = 3;
  string phone      = 4;
  int64  created_at = 5; // Unix timestamp
}

// Request/Response messages
message GetUserRequest {
  string user_id = 1;
}

message GetUserResponse {
  User   user  = 1;
  string error = 2; // empty string means no error
}

message CreateUserRequest {
  string name  = 1;
  string email = 2;
  string phone = 3;
}

message CreateUserResponse {
  User   user  = 1;
  string error = 2;
}

// The gRPC service definition
service UserService {
  // GetUser retrieves a user by ID
  rpc GetUser(GetUserRequest) returns (GetUserResponse);

  // CreateUser creates a new user account
  rpc CreateUser(CreateUserRequest) returns (CreateUserResponse);
}
```

### 1.2 Generating Go Code

Install the required tools once:

```bash
# Install protoc (macOS)
brew install protobuf

# Install Go plugins
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

# Ensure GOPATH/bin is in PATH
export PATH="$PATH:$(go env GOPATH)/bin"
```

Generate the code from the proto directory:

```bash
# From the project root
protoc \
  --go_out=. \
  --go_opt=paths=source_relative \
  --go-grpc_out=. \
  --go-grpc_opt=paths=source_relative \
  proto/user/user.proto
```

This generates two files:
- `proto/user/user.pb.go` — message types (User, GetUserRequest, etc.)
- `proto/user/user_grpc.pb.go` — service client and server interfaces

### 1.3 What the Generated Code Looks Like

The generator creates a server interface you must implement:

```go
// Generated in user_grpc.pb.go — DO NOT EDIT
type UserServiceServer interface {
    GetUser(context.Context, *GetUserRequest) (*GetUserResponse, error)
    CreateUser(context.Context, *CreateUserRequest) (*CreateUserResponse, error)
    mustEmbedUnimplementedUserServiceServer()
}

// UnimplementedUserServiceServer — embed this to get forward compatibility
type UnimplementedUserServiceServer struct{}

func (UnimplementedUserServiceServer) GetUser(context.Context, *GetUserRequest) (*GetUserResponse, error) {
    return nil, status.Errorf(codes.Unimplemented, "method GetUser not implemented")
}
```

And a client:

```go
// Generated client — use this in order-service
type UserServiceClient interface {
    GetUser(ctx context.Context, in *GetUserRequest, opts ...grpc.CallOption) (*GetUserResponse, error)
    CreateUser(ctx context.Context, in *CreateUserRequest, opts ...grpc.CallOption) (*CreateUserResponse, error)
}
```

---

## Part 2: user-service — gRPC Server

### 2.1 go.mod for user-service

```
module github.com/yourorg/microservices/user-service

go 1.22

require (
    google.golang.org/grpc v1.63.0
    google.golang.org/protobuf v1.34.0
    github.com/google/uuid v1.6.0
)
```

### 2.2 server.go — Business Logic

```go
// user-service/server.go
package main

import (
    "context"
    "fmt"
    "log/slog"
    "sync"
    "time"

    "github.com/google/uuid"
    pb "github.com/yourorg/microservices/proto/user"
    "google.golang.org/grpc/codes"
    "google.golang.org/grpc/status"
)

// userStore is a simple in-memory store. In production: use a real DB.
type userStore struct {
    mu    sync.RWMutex
    users map[string]*pb.User
}

func newUserStore() *userStore {
    s := &userStore{
        users: make(map[string]*pb.User),
    }
    // Seed with test data
    s.users["user-1"] = &pb.User{
        Id:        "user-1",
        Name:      "Alice Johnson",
        Email:     "alice@example.com",
        Phone:     "+1-555-0100",
        CreatedAt: time.Now().Unix(),
    }
    return s
}

// UserServiceServer implements the generated pb.UserServiceServer interface.
// We embed UnimplementedUserServiceServer for forward compatibility.
type UserServiceServer struct {
    pb.UnimplementedUserServiceServer
    store  *userStore
    logger *slog.Logger
}

func NewUserServiceServer(logger *slog.Logger) *UserServiceServer {
    return &UserServiceServer{
        store:  newUserStore(),
        logger: logger,
    }
}

// GetUser handles the GetUser RPC.
func (s *UserServiceServer) GetUser(ctx context.Context, req *pb.GetUserRequest) (*pb.GetUserResponse, error) {
    s.logger.Info("GetUser called", "user_id", req.UserId)

    // Validate input
    if req.UserId == "" {
        return nil, status.Error(codes.InvalidArgument, "user_id is required")
    }

    s.store.mu.RLock()
    user, ok := s.store.users[req.UserId]
    s.store.mu.RUnlock()

    if !ok {
        return nil, status.Errorf(codes.NotFound, "user %s not found", req.UserId)
    }

    return &pb.GetUserResponse{User: user}, nil
}

// CreateUser handles the CreateUser RPC.
func (s *UserServiceServer) CreateUser(ctx context.Context, req *pb.CreateUserRequest) (*pb.CreateUserResponse, error) {
    s.logger.Info("CreateUser called", "email", req.Email)

    // Validate input
    if req.Name == "" || req.Email == "" {
        return nil, status.Error(codes.InvalidArgument, "name and email are required")
    }

    // Check for duplicate email
    s.store.mu.RLock()
    for _, u := range s.store.users {
        if u.Email == req.Email {
            s.store.mu.RUnlock()
            return nil, status.Errorf(codes.AlreadyExists, "email %s already registered", req.Email)
        }
    }
    s.store.mu.RUnlock()

    user := &pb.User{
        Id:        fmt.Sprintf("user-%s", uuid.New().String()[:8]),
        Name:      req.Name,
        Email:     req.Email,
        Phone:     req.Phone,
        CreatedAt: time.Now().Unix(),
    }

    s.store.mu.Lock()
    s.store.users[user.Id] = user
    s.store.mu.Unlock()

    s.logger.Info("User created", "id", user.Id, "email", user.Email)

    return &pb.CreateUserResponse{User: user}, nil
}
```

### 2.3 main.go — Server Setup with Interceptors

```go
// user-service/main.go
package main

import (
    "context"
    "log/slog"
    "net"
    "net/http"
    "os"
    "os/signal"
    "runtime/debug"
    "syscall"
    "time"

    pb "github.com/yourorg/microservices/proto/user"
    "google.golang.org/grpc"
    "google.golang.org/grpc/codes"
    "google.golang.org/grpc/health"
    "google.golang.org/grpc/health/grpc_health_v1"
    "google.golang.org/grpc/metadata"
    "google.golang.org/grpc/reflection"
    "google.golang.org/grpc/status"
)

func main() {
    logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
        Level: slog.LevelInfo,
    }))

    port := getEnv("GRPC_PORT", "50051")
    httpPort := getEnv("HTTP_PORT", "8081")

    // Build the gRPC server with interceptors
    grpcServer := grpc.NewServer(
        grpc.ChainUnaryInterceptor(
            loggingInterceptor(logger),
            recoveryInterceptor(logger),
            authInterceptor,
        ),
    )

    // Register our service implementation
    userSrv := NewUserServiceServer(logger)
    pb.RegisterUserServiceServer(grpcServer, userSrv)

    // Register gRPC health check (standard protocol)
    healthSrv := health.NewServer()
    grpc_health_v1.RegisterHealthServer(grpcServer, healthSrv)
    healthSrv.SetServingStatus("user.UserService", grpc_health_v1.HealthCheckResponse_SERVING)

    // Register reflection service — lets grpcurl introspect the server
    reflection.Register(grpcServer)

    // Start gRPC listener
    lis, err := net.Listen("tcp", ":"+port)
    if err != nil {
        logger.Error("failed to listen", "error", err)
        os.Exit(1)
    }

    // Start HTTP health endpoint in background (useful for k8s liveness probes)
    go startHTTPHealth(httpPort, logger)

    // Graceful shutdown
    quit := make(chan os.Signal, 1)
    signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

    go func() {
        logger.Info("user-service gRPC listening", "port", port)
        if err := grpcServer.Serve(lis); err != nil {
            logger.Error("gRPC server failed", "error", err)
        }
    }()

    <-quit
    logger.Info("shutting down user-service...")

    // GracefulStop waits for in-flight RPCs to complete (up to a deadline)
    stopped := make(chan struct{})
    go func() {
        grpcServer.GracefulStop()
        close(stopped)
    }()

    select {
    case <-stopped:
        logger.Info("gRPC server stopped gracefully")
    case <-time.After(10 * time.Second):
        logger.Warn("graceful stop timed out, forcing stop")
        grpcServer.Stop()
    }
}

// loggingInterceptor logs every incoming RPC: method, duration, status code.
func loggingInterceptor(logger *slog.Logger) grpc.UnaryServerInterceptor {
    return func(
        ctx context.Context,
        req interface{},
        info *grpc.UnaryServerInfo,
        handler grpc.UnaryHandler,
    ) (interface{}, error) {
        start := time.Now()

        resp, err := handler(ctx, req)

        code := codes.OK
        if err != nil {
            code = status.Code(err)
        }

        logger.Info("RPC",
            "method", info.FullMethod,
            "duration_ms", time.Since(start).Milliseconds(),
            "code", code.String(),
        )

        return resp, err
    }
}

// recoveryInterceptor catches panics in handlers and converts them to gRPC Internal errors.
func recoveryInterceptor(logger *slog.Logger) grpc.UnaryServerInterceptor {
    return func(
        ctx context.Context,
        req interface{},
        info *grpc.UnaryServerInfo,
        handler grpc.UnaryHandler,
    ) (resp interface{}, err error) {
        defer func() {
            if r := recover(); r != nil {
                logger.Error("panic in RPC handler",
                    "method", info.FullMethod,
                    "panic", r,
                    "stack", string(debug.Stack()),
                )
                err = status.Errorf(codes.Internal, "internal server error")
            }
        }()
        return handler(ctx, req)
    }
}

// authInterceptor validates a Bearer token from metadata.
// In production: verify a JWT. Here we check for a static token to show the pattern.
func authInterceptor(
    ctx context.Context,
    req interface{},
    info *grpc.UnaryServerInfo,
    handler grpc.UnaryHandler,
) (interface{}, error) {
    // Skip auth for health checks
    if info.FullMethod == "/grpc.health.v1.Health/Check" {
        return handler(ctx, req)
    }

    md, ok := metadata.FromIncomingContext(ctx)
    if !ok {
        return nil, status.Error(codes.Unauthenticated, "metadata missing")
    }

    values := md["authorization"]
    if len(values) == 0 {
        return nil, status.Error(codes.Unauthenticated, "authorization token missing")
    }

    token := values[0]
    expectedToken := "Bearer " + getEnv("SERVICE_TOKEN", "secret-service-token")
    if token != expectedToken {
        return nil, status.Error(codes.Unauthenticated, "invalid token")
    }

    return handler(ctx, req)
}

// startHTTPHealth runs a minimal HTTP server for /health (used by load balancers, k8s).
func startHTTPHealth(port string, logger *slog.Logger) {
    mux := http.NewServeMux()
    mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(http.StatusOK)
        w.Write([]byte(`{"status":"ok","service":"user-service"}`))
    })

    srv := &http.Server{
        Addr:    ":" + port,
        Handler: mux,
    }

    logger.Info("HTTP health listening", "port", port)
    if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
        logger.Error("HTTP health server failed", "error", err)
    }
}

func getEnv(key, defaultVal string) string {
    if v := os.Getenv(key); v != "" {
        return v
    }
    return defaultVal
}
```

---

## Part 3: order-service — gRPC Client + Kafka Producer

### 3.1 go.mod for order-service

```
module github.com/yourorg/microservices/order-service

go 1.22

require (
    google.golang.org/grpc v1.63.0
    google.golang.org/protobuf v1.34.0
    github.com/segmentio/kafka-go v0.4.47
    github.com/google/uuid v1.6.0
)
```

### 3.2 kafka_producer.go — Publishing OrderCreated Events

```go
// order-service/kafka_producer.go
package main

import (
    "context"
    "encoding/json"
    "fmt"
    "log/slog"
    "time"

    "github.com/segmentio/kafka-go"
)

// OrderEvent is the event payload published to Kafka.
// Keep events small and self-contained — consumers should not need to call back.
type OrderEvent struct {
    EventType string    `json:"event_type"` // "order.created", "order.cancelled"
    OrderID   string    `json:"order_id"`
    UserID    string    `json:"user_id"`
    UserEmail string    `json:"user_email"`
    UserName  string    `json:"user_name"`
    Items     []Item    `json:"items"`
    Total     float64   `json:"total"`
    OccurredAt time.Time `json:"occurred_at"`
}

// KafkaProducer wraps kafka-go's Writer with our business logic.
type KafkaProducer struct {
    writer *kafka.Writer
    logger *slog.Logger
}

// NewKafkaProducer creates a new producer. brokers is comma-separated, e.g. "localhost:9092".
func NewKafkaProducer(brokers []string, topic string, logger *slog.Logger) *KafkaProducer {
    writer := &kafka.Writer{
        Addr:  kafka.TCP(brokers...),
        Topic: topic,

        // Balancer determines which partition to write to.
        // LeastBytes sends to the partition with fewest buffered bytes.
        // RoundRobin or Hash(key) are alternatives.
        Balancer: &kafka.LeastBytes{},

        // Batching: collect up to 100 messages or wait 10ms before flushing.
        // Increases throughput at the cost of slight latency.
        BatchSize:    100,
        BatchTimeout: 10 * time.Millisecond,

        // RequiredAcks: how many brokers must ack before WriteMessages returns.
        // RequireAll (-1) is safest for important events.
        RequiredAcks: kafka.RequireAll,

        // Async: if true, WriteMessages returns immediately (fire-and-forget).
        // For order events, we want synchronous writes to guarantee delivery.
        Async: false,

        // Compression reduces network and storage usage.
        Compression: kafka.Snappy,

        // Logger
        Logger:      kafka.LoggerFunc(func(msg string, a ...interface{}) { logger.Debug(fmt.Sprintf(msg, a...)) }),
        ErrorLogger: kafka.LoggerFunc(func(msg string, a ...interface{}) { logger.Error(fmt.Sprintf(msg, a...)) }),
    }

    return &KafkaProducer{writer: writer, logger: logger}
}

// PublishOrderCreated publishes an OrderCreated event synchronously.
// Returns an error if the message could not be delivered within ctx deadline.
func (p *KafkaProducer) PublishOrderCreated(ctx context.Context, event OrderEvent) error {
    event.EventType = "order.created"
    event.OccurredAt = time.Now().UTC()

    payload, err := json.Marshal(event)
    if err != nil {
        return fmt.Errorf("marshal order event: %w", err)
    }

    // Key = OrderID ensures all events for the same order go to the same partition.
    // This preserves ordering per order.
    msg := kafka.Message{
        Key:   []byte(event.OrderID),
        Value: payload,
        Headers: []kafka.Header{
            {Key: "event-type", Value: []byte(event.EventType)},
            {Key: "content-type", Value: []byte("application/json")},
        },
    }

    // WriteMessages blocks until acked (Async: false).
    // Use a context with timeout to avoid hanging forever.
    writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
    defer cancel()

    if err := p.writer.WriteMessages(writeCtx, msg); err != nil {
        return fmt.Errorf("write kafka message: %w", err)
    }

    p.logger.Info("OrderCreated event published",
        "order_id", event.OrderID,
        "user_id", event.UserID,
        "topic", p.writer.Topic,
    )

    return nil
}

// Close flushes pending messages and closes the connection.
func (p *KafkaProducer) Close() error {
    return p.writer.Close()
}
```

### 3.3 handler.go — HTTP Handler: the Full Flow

```go
// order-service/handler.go
package main

import (
    "context"
    "encoding/json"
    "fmt"
    "log/slog"
    "net/http"
    "time"

    "github.com/google/uuid"
    pb "github.com/yourorg/microservices/proto/user"
)

// Item represents a single line item in an order.
type Item struct {
    ProductID string  `json:"product_id"`
    Name      string  `json:"name"`
    Quantity  int     `json:"quantity"`
    Price     float64 `json:"price"`
}

// CreateOrderRequest is the HTTP request body for POST /orders.
type CreateOrderRequest struct {
    UserID string `json:"user_id"`
    Items  []Item `json:"items"`
}

// CreateOrderResponse is the HTTP response for a successful order.
type CreateOrderResponse struct {
    OrderID string  `json:"order_id"`
    UserID  string  `json:"user_id"`
    Total   float64 `json:"total"`
    Status  string  `json:"status"`
}

// OrderHandler holds the dependencies for HTTP handlers.
type OrderHandler struct {
    userClient pb.UserServiceClient // gRPC client
    producer   *KafkaProducer
    logger     *slog.Logger
}

func NewOrderHandler(userClient pb.UserServiceClient, producer *KafkaProducer, logger *slog.Logger) *OrderHandler {
    return &OrderHandler{
        userClient: userClient,
        producer:   producer,
        logger:     logger,
    }
}

// CreateOrder is the main POST /orders handler.
// Flow: validate → call user-service via gRPC → calculate total → publish to Kafka → respond
func (h *OrderHandler) CreateOrder(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()

    // 1. Parse and validate request
    var req CreateOrderRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        respondError(w, http.StatusBadRequest, "invalid request body")
        return
    }

    if req.UserID == "" {
        respondError(w, http.StatusBadRequest, "user_id is required")
        return
    }
    if len(req.Items) == 0 {
        respondError(w, http.StatusBadRequest, "at least one item is required")
        return
    }

    // 2. Validate user exists via gRPC call to user-service
    // Use a tight deadline — fail fast, don't block the HTTP request
    grpcCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
    defer cancel()

    userResp, err := h.userClient.GetUser(grpcCtx, &pb.GetUserRequest{UserId: req.UserID})
    if err != nil {
        h.logger.Error("failed to get user from user-service",
            "user_id", req.UserID,
            "error", err,
        )
        // Translate gRPC status codes to HTTP status codes
        respondGRPCError(w, err)
        return
    }

    user := userResp.User

    // 3. Calculate order total
    var total float64
    for _, item := range req.Items {
        if item.Quantity <= 0 || item.Price <= 0 {
            respondError(w, http.StatusBadRequest, fmt.Sprintf("invalid item: %s", item.ProductID))
            return
        }
        total += float64(item.Quantity) * item.Price
    }

    // 4. Generate order ID
    orderID := fmt.Sprintf("order-%s", uuid.New().String()[:8])

    // 5. Publish OrderCreated event to Kafka
    event := OrderEvent{
        OrderID:   orderID,
        UserID:    user.Id,
        UserEmail: user.Email,
        UserName:  user.Name,
        Items:     req.Items,
        Total:     total,
    }

    if err := h.producer.PublishOrderCreated(ctx, event); err != nil {
        h.logger.Error("failed to publish order event",
            "order_id", orderID,
            "error", err,
        )
        // Decide: fail the request or accept the order and retry later?
        // For money-related operations, we fail here. Use outbox pattern for production.
        respondError(w, http.StatusInternalServerError, "failed to process order")
        return
    }

    // 6. Respond with success
    h.logger.Info("Order created successfully",
        "order_id", orderID,
        "user_id", user.Id,
        "total", total,
    )

    respondJSON(w, http.StatusCreated, CreateOrderResponse{
        OrderID: orderID,
        UserID:  user.Id,
        Total:   total,
        Status:  "pending",
    })
}

// HealthCheck is the GET /health handler.
func (h *OrderHandler) HealthCheck(w http.ResponseWriter, r *http.Request) {
    respondJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "order-service"})
}

// --- helpers ---

func respondJSON(w http.ResponseWriter, code int, v interface{}) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(code)
    json.NewEncoder(w).Encode(v)
}

func respondError(w http.ResponseWriter, code int, msg string) {
    respondJSON(w, code, map[string]string{"error": msg})
}

// respondGRPCError maps gRPC status codes to HTTP status codes.
func respondGRPCError(w http.ResponseWriter, err error) {
    // Import: google.golang.org/grpc/status, google.golang.org/grpc/codes
    // Using string comparison here for brevity; in real code use status.Code(err)
    msg := err.Error()
    switch {
    case containsCode(err, "NotFound"):
        respondError(w, http.StatusNotFound, "user not found")
    case containsCode(err, "InvalidArgument"):
        respondError(w, http.StatusBadRequest, "invalid request")
    case containsCode(err, "Unauthenticated"):
        respondError(w, http.StatusUnauthorized, "unauthorized")
    case containsCode(err, "DeadlineExceeded"):
        respondError(w, http.StatusGatewayTimeout, "upstream timeout")
    default:
        _ = msg
        respondError(w, http.StatusInternalServerError, "internal error")
    }
}

func containsCode(err error, code string) bool {
    return err != nil && len(err.Error()) > 0 &&
        (len(code) == 0 || containsString(err.Error(), code))
}

func containsString(s, sub string) bool {
    return len(s) >= len(sub) && (s == sub || len(s) > 0 && searchString(s, sub))
}

func searchString(s, sub string) bool {
    for i := 0; i <= len(s)-len(sub); i++ {
        if s[i:i+len(sub)] == sub {
            return true
        }
    }
    return false
}
```

### 3.4 main.go — gRPC Client with Connection Pooling & Retry

```go
// order-service/main.go
package main

import (
    "log/slog"
    "net/http"
    "os"
    "os/signal"
    "syscall"
    "time"

    pb "github.com/yourorg/microservices/proto/user"
    "google.golang.org/grpc"
    "google.golang.org/grpc/credentials/insecure"
    "google.golang.org/grpc/keepalive"
    "google.golang.org/grpc/metadata"
    "strings"
    "context"
)

func main() {
    logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

    // --- gRPC client setup ---
    userServiceAddr := getEnv("USER_SERVICE_ADDR", "localhost:50051")
    serviceToken := getEnv("SERVICE_TOKEN", "secret-service-token")

    // Retry policy: automatically retry on transient failures.
    // This is a JSON service config — gRPC understands it natively.
    retryPolicy := `{
        "methodConfig": [{
            "name": [{"service": "user.UserService"}],
            "retryPolicy": {
                "maxAttempts": 3,
                "initialBackoff": "0.1s",
                "maxBackoff": "1s",
                "backoffMultiplier": 2,
                "retryableStatusCodes": ["UNAVAILABLE", "DEADLINE_EXCEEDED"]
            },
            "timeout": "5s"
        }]
    }`

    conn, err := grpc.NewClient(
        userServiceAddr,
        // Use insecure for local/internal comms. In production: use TLS.
        grpc.WithTransportCredentials(insecure.NewCredentials()),

        // Service config with retry policy
        grpc.WithDefaultServiceConfig(retryPolicy),

        // Keepalive: send pings to keep the connection alive through firewalls/load balancers
        grpc.WithKeepaliveParams(keepalive.ClientParameters{
            Time:                10 * time.Second, // send ping after 10s of inactivity
            Timeout:             3 * time.Second,  // wait 3s for pong before considering dead
            PermitWithoutStream: true,
        }),

        // Per-call auth: inject the service token into every outgoing RPC's metadata
        grpc.WithUnaryInterceptor(func(
            ctx context.Context,
            method string,
            req, reply interface{},
            cc *grpc.ClientConn,
            invoker grpc.UnaryInvoker,
            opts ...grpc.CallOption,
        ) error {
            ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+serviceToken)
            return invoker(ctx, method, req, reply, cc, opts...)
        }),
    )
    if err != nil {
        logger.Error("failed to connect to user-service", "error", err)
        os.Exit(1)
    }
    defer conn.Close()

    userClient := pb.NewUserServiceClient(conn)

    // --- Kafka producer setup ---
    kafkaBrokers := strings.Split(getEnv("KAFKA_BROKERS", "localhost:9092"), ",")
    producer := NewKafkaProducer(kafkaBrokers, "order-events", logger)
    defer producer.Close()

    // --- HTTP server ---
    handler := NewOrderHandler(userClient, producer, logger)

    mux := http.NewServeMux()
    mux.HandleFunc("POST /orders", handler.CreateOrder)
    mux.HandleFunc("GET /health", handler.HealthCheck)

    httpPort := getEnv("HTTP_PORT", "8080")
    server := &http.Server{
        Addr:         ":" + httpPort,
        Handler:      mux,
        ReadTimeout:  15 * time.Second,
        WriteTimeout: 15 * time.Second,
        IdleTimeout:  60 * time.Second,
    }

    // Graceful shutdown
    quit := make(chan os.Signal, 1)
    signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

    go func() {
        logger.Info("order-service HTTP listening", "port", httpPort)
        if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
            logger.Error("HTTP server failed", "error", err)
        }
    }()

    <-quit
    logger.Info("shutting down order-service...")

    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()

    if err := server.Shutdown(ctx); err != nil {
        logger.Error("HTTP server shutdown error", "error", err)
    }

    logger.Info("order-service stopped")
}
```

**Key gRPC client concepts:**

| Concept | What it does |
|---|---|
| `grpc.NewClient` | Creates a connection (lazy dial) |
| `insecure.NewCredentials()` | No TLS — fine for internal mesh, not for internet |
| `retryPolicy` | Automatic retries on UNAVAILABLE/DEADLINE_EXCEEDED |
| `keepalive` | Detects dead connections faster than TCP defaults |
| `UnaryInterceptor` | Injects auth token into every outgoing call |

---

## Part 4: notification-service — Kafka Consumer

### 4.1 go.mod for notification-service

```
module github.com/yourorg/microservices/notification-service

go 1.22

require (
    github.com/segmentio/kafka-go v0.4.47
)
```

### 4.2 consumer.go — Consumer Group with Manual Commits

```go
// notification-service/consumer.go
package main

import (
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "io"
    "log/slog"
    "time"

    "github.com/segmentio/kafka-go"
)

// OrderEvent mirrors the producer's event struct.
// In production: share this type via a separate proto or shared module.
type OrderEvent struct {
    EventType  string    `json:"event_type"`
    OrderID    string    `json:"order_id"`
    UserID     string    `json:"user_id"`
    UserEmail  string    `json:"user_email"`
    UserName   string    `json:"user_name"`
    Total      float64   `json:"total"`
    OccurredAt time.Time `json:"occurred_at"`
}

// NotificationService is the consumer that processes order events.
type NotificationService struct {
    reader *kafka.Reader
    logger *slog.Logger
}

// NewNotificationService creates a consumer using a consumer group.
// Consumer groups allow horizontal scaling: each instance gets a subset of partitions.
func NewNotificationService(brokers []string, topic, groupID string, logger *slog.Logger) *NotificationService {
    reader := kafka.NewReader(kafka.ReaderConfig{
        Brokers: brokers,
        Topic:   topic,
        GroupID: groupID, // consumer group — enables partition rebalancing

        // MinBytes / MaxBytes: tuning for throughput vs latency
        MinBytes: 1e3,  // 1KB minimum — wait for at least this much data
        MaxBytes: 10e6, // 10MB maximum per fetch

        // MaxWait: how long to wait before returning if MinBytes not reached
        MaxWait: 500 * time.Millisecond,

        // CommitInterval: 0 means manual commits (we control exactly when offset advances)
        // This prevents message loss on crash but can cause duplicates on restart.
        // Design consumers to be idempotent!
        CommitInterval: 0,

        // StartOffset: what to do if no committed offset exists for this group.
        // LastOffset = start from latest (skip old messages on first run)
        // FirstOffset = start from beginning
        StartOffset: kafka.LastOffset,

        Logger:      kafka.LoggerFunc(func(msg string, a ...interface{}) { logger.Debug(fmt.Sprintf(msg, a...)) }),
        ErrorLogger: kafka.LoggerFunc(func(msg string, a ...interface{}) { logger.Error(fmt.Sprintf(msg, a...)) }),
    })

    return &NotificationService{
        reader: reader,
        logger: logger,
    }
}

// Run starts the consumer loop. It blocks until ctx is cancelled.
func (s *NotificationService) Run(ctx context.Context) error {
    s.logger.Info("notification-service consumer started")

    for {
        // FetchMessage retrieves one message. Blocks until a message is available or ctx done.
        msg, err := s.reader.FetchMessage(ctx)
        if err != nil {
            if errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) {
                s.logger.Info("consumer context cancelled, stopping")
                return nil
            }
            s.logger.Error("failed to fetch message", "error", err)
            // Back off before retrying to avoid hammering Kafka on errors
            time.Sleep(1 * time.Second)
            continue
        }

        s.logger.Debug("message received",
            "topic", msg.Topic,
            "partition", msg.Partition,
            "offset", msg.Offset,
            "key", string(msg.Key),
        )

        // Process the message
        if err := s.processMessage(ctx, msg); err != nil {
            s.logger.Error("failed to process message",
                "offset", msg.Offset,
                "error", err,
            )
            // Dead letter queue pattern: in production, publish failed messages
            // to a separate topic (e.g. "order-events-dlq") for manual inspection.
            // For now: log and commit anyway to avoid infinite loop on poison messages.
        }

        // Commit AFTER processing.
        // This means: if we crash between processing and committing, we'll reprocess.
        // That's safer than committing first (which risks missing a message).
        // => Design your handlers to be idempotent!
        if err := s.reader.CommitMessages(ctx, msg); err != nil {
            s.logger.Error("failed to commit offset", "offset", msg.Offset, "error", err)
            // Don't return — we processed the message, try to commit again on the next loop
        }
    }
}

// processMessage routes the event to the appropriate handler based on event_type.
func (s *NotificationService) processMessage(ctx context.Context, msg kafka.Message) error {
    var event OrderEvent
    if err := json.Unmarshal(msg.Value, &event); err != nil {
        // Malformed JSON is a poison message — we can't fix it, so log and skip.
        s.logger.Error("failed to unmarshal event",
            "offset", msg.Offset,
            "raw", string(msg.Value),
            "error", err,
        )
        return nil // return nil to commit (skip) the bad message
    }

    switch event.EventType {
    case "order.created":
        return s.handleOrderCreated(ctx, event)
    default:
        s.logger.Warn("unknown event type", "event_type", event.EventType)
        return nil // commit and skip unknown events
    }
}

// handleOrderCreated sends a confirmation notification when an order is placed.
func (s *NotificationService) handleOrderCreated(ctx context.Context, event OrderEvent) error {
    s.logger.Info("sending order confirmation",
        "order_id", event.OrderID,
        "user_email", event.UserEmail,
        "total", event.Total,
    )

    // Simulate sending an email/SMS notification
    // In production: call SendGrid, Twilio, SNS, etc.
    notification := fmt.Sprintf(
        "Hi %s! Your order %s has been received. Total: $%.2f",
        event.UserName,
        event.OrderID,
        event.Total,
    )

    // Simulate network call with timeout
    sendCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
    defer cancel()

    if err := sendNotification(sendCtx, event.UserEmail, notification); err != nil {
        return fmt.Errorf("send notification for order %s: %w", event.OrderID, err)
    }

    s.logger.Info("notification sent",
        "order_id", event.OrderID,
        "recipient", event.UserEmail,
    )

    return nil
}

// sendNotification simulates sending an external notification.
// Replace with real email/SMS client in production.
func sendNotification(ctx context.Context, email, message string) error {
    // Simulate 50ms network latency
    select {
    case <-ctx.Done():
        return ctx.Err()
    case <-time.After(50 * time.Millisecond):
    }

    fmt.Printf("[EMAIL] To: %s\nMessage: %s\n\n", email, message)
    return nil
}

// Close cleanly shuts down the consumer reader.
func (s *NotificationService) Close() error {
    return s.reader.Close()
}
```

### 4.3 main.go — Graceful Shutdown

```go
// notification-service/main.go
package main

import (
    "context"
    "log/slog"
    "net/http"
    "os"
    "os/signal"
    "strings"
    "syscall"
    "time"
)

func main() {
    logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

    kafkaBrokers := strings.Split(getEnv("KAFKA_BROKERS", "localhost:9092"), ",")
    topic := getEnv("KAFKA_TOPIC", "order-events")
    groupID := getEnv("KAFKA_GROUP_ID", "notification-service-v1")

    svc := NewNotificationService(kafkaBrokers, topic, groupID, logger)
    defer svc.Close()

    // Context tied to OS signals — cancellation triggers graceful shutdown
    ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
    defer stop()

    // HTTP health check
    go func() {
        mux := http.NewServeMux()
        mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
            w.Header().Set("Content-Type", "application/json")
            w.WriteHeader(http.StatusOK)
            w.Write([]byte(`{"status":"ok","service":"notification-service"}`))
        })
        srv := &http.Server{Addr: ":" + getEnv("HTTP_PORT", "8082"), Handler: mux}
        logger.Info("HTTP health listening", "port", getEnv("HTTP_PORT", "8082"))
        srv.ListenAndServe()
    }()

    // Run the consumer. Blocks until ctx is done.
    if err := svc.Run(ctx); err != nil {
        logger.Error("consumer stopped with error", "error", err)
        os.Exit(1)
    }

    // Give in-flight processing up to 5 seconds to finish
    shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()

    logger.Info("notification-service shutting down gracefully...")
    <-shutdownCtx.Done()
    logger.Info("notification-service stopped")
}

func getEnv(key, defaultVal string) string {
    if v := os.Getenv(key); v != "" {
        return v
    }
    return defaultVal
}
```

---

## Part 5: Docker Compose — Running Everything Locally

```yaml
# docker-compose.yml
version: "3.9"

services:
  # ── Infrastructure ──────────────────────────────────────────────

  zookeeper:
    image: confluentinc/cp-zookeeper:7.6.0
    environment:
      ZOOKEEPER_CLIENT_PORT: 2181
      ZOOKEEPER_TICK_TIME: 2000
    ports:
      - "2181:2181"

  kafka:
    image: confluentinc/cp-kafka:7.6.0
    depends_on:
      - zookeeper
    ports:
      - "9092:9092"
    environment:
      KAFKA_BROKER_ID: 1
      KAFKA_ZOOKEEPER_CONNECT: zookeeper:2181
      # PLAINTEXT_HOST makes Kafka reachable from your host machine on localhost:9092
      KAFKA_LISTENER_SECURITY_PROTOCOL_MAP: PLAINTEXT:PLAINTEXT,PLAINTEXT_HOST:PLAINTEXT
      KAFKA_ADVERTISED_LISTENERS: PLAINTEXT://kafka:29092,PLAINTEXT_HOST://localhost:9092
      KAFKA_INTER_BROKER_LISTENER_NAME: PLAINTEXT
      KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR: 1
      KAFKA_AUTO_CREATE_TOPICS_ENABLE: "true"
    healthcheck:
      test: ["CMD", "kafka-topics", "--bootstrap-server", "localhost:9092", "--list"]
      interval: 10s
      timeout: 5s
      retries: 5

  # ZooNavigator — web UI for inspecting Kafka/Zookeeper (http://localhost:9000)
  zoonavigator:
    image: elkozmon/zoonavigator:latest
    ports:
      - "9000:9000"
    depends_on:
      - zookeeper

  # ── Application Services ─────────────────────────────────────────

  user-service:
    build:
      context: ./user-service
      dockerfile: Dockerfile
    ports:
      - "50051:50051"
      - "8081:8081"
    environment:
      GRPC_PORT: "50051"
      HTTP_PORT: "8081"
      SERVICE_TOKEN: "secret-service-token"
    healthcheck:
      test: ["CMD", "grpc_health_probe", "-addr=:50051"]
      interval: 10s
      timeout: 3s
      retries: 3

  order-service:
    build:
      context: ./order-service
      dockerfile: Dockerfile
    ports:
      - "8080:8080"
    environment:
      HTTP_PORT: "8080"
      USER_SERVICE_ADDR: "user-service:50051"
      SERVICE_TOKEN: "secret-service-token"
      KAFKA_BROKERS: "kafka:29092"
    depends_on:
      user-service:
        condition: service_healthy
      kafka:
        condition: service_healthy

  notification-service:
    build:
      context: ./notification-service
      dockerfile: Dockerfile
    ports:
      - "8082:8082"
    environment:
      HTTP_PORT: "8082"
      KAFKA_BROKERS: "kafka:29092"
      KAFKA_TOPIC: "order-events"
      KAFKA_GROUP_ID: "notification-service-v1"
    depends_on:
      kafka:
        condition: service_healthy
```

### Dockerfile (same pattern for all 3 services)

```dockerfile
# Multi-stage build: builder stage compiles, final stage is minimal
FROM golang:1.22-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o service .

# Final stage: scratch or distroless for minimal attack surface
FROM gcr.io/distroless/static-debian12

COPY --from=builder /app/service /service

ENTRYPOINT ["/service"]
```

### Running the System

```bash
# Start everything
docker compose up -d

# Check health
curl http://localhost:8081/health  # user-service
curl http://localhost:8080/health  # order-service
curl http://localhost:8082/health  # notification-service

# Create an order (the full flow!)
curl -X POST http://localhost:8080/orders \
  -H "Content-Type: application/json" \
  -d '{
    "user_id": "user-1",
    "items": [
      {"product_id": "prod-1", "name": "Go Book", "quantity": 2, "price": 29.99}
    ]
  }'

# Watch notification-service logs to see the Kafka event processed
docker compose logs -f notification-service

# Tear down
docker compose down -v
```

---

## Part 6: Service Discovery Pattern

For local development, we use environment variables to configure service addresses. This is the simplest approach and aligns with the 12-factor app methodology.

```go
// Pattern: read address from environment at startup
userServiceAddr := os.Getenv("USER_SERVICE_ADDR")
if userServiceAddr == "" {
    userServiceAddr = "localhost:50051" // sensible default for local dev
}
```

**Scaling up: when to use a service registry:**

| Approach | When to use |
|---|---|
| Env vars (current) | Docker Compose, Kubernetes (env injected from Services) |
| Kubernetes Services | Any k8s deployment — DNS-based discovery built in |
| Consul | Multi-datacenter, non-k8s environments |
| etcd | Already using it (e.g., with etcd-backed systems) |

**Consul example** (for reference):

```go
// go get github.com/hashicorp/consul/api
import "github.com/hashicorp/consul/api"

func discoverService(name string) (string, error) {
    client, _ := api.NewDefaultClient()
    services, _, err := client.Health().Service(name, "", true, nil)
    if err != nil || len(services) == 0 {
        return "", fmt.Errorf("service %s not found", name)
    }
    svc := services[0].Service
    return fmt.Sprintf("%s:%d", svc.Address, svc.Port), nil
}
```

---

## Part 7: Testing the Services

### Testing the gRPC Server (unit test, no network)

```go
// user-service/server_test.go
package main

import (
    "context"
    "testing"

    pb "github.com/yourorg/microservices/proto/user"
    "google.golang.org/grpc/codes"
    "google.golang.org/grpc/status"
    "log/slog"
    "os"
)

func TestGetUser_Found(t *testing.T) {
    srv := NewUserServiceServer(slog.New(slog.NewTextHandler(os.Stderr, nil)))

    resp, err := srv.GetUser(context.Background(), &pb.GetUserRequest{UserId: "user-1"})
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }

    if resp.User.Id != "user-1" {
        t.Errorf("expected user-1, got %s", resp.User.Id)
    }
}

func TestGetUser_NotFound(t *testing.T) {
    srv := NewUserServiceServer(slog.New(slog.NewTextHandler(os.Stderr, nil)))

    _, err := srv.GetUser(context.Background(), &pb.GetUserRequest{UserId: "nonexistent"})
    if err == nil {
        t.Fatal("expected error, got nil")
    }

    st, _ := status.FromError(err)
    if st.Code() != codes.NotFound {
        t.Errorf("expected NotFound, got %s", st.Code())
    }
}
```

### Integration Test with a Real gRPC Connection

```go
// user-service/integration_test.go
//go:build integration

package main

import (
    "context"
    "net"
    "testing"

    pb "github.com/yourorg/microservices/proto/user"
    "google.golang.org/grpc"
    "google.golang.org/grpc/credentials/insecure"
    "log/slog"
    "os"
)

func TestGetUser_Integration(t *testing.T) {
    // Start the real server on a random port
    lis, err := net.Listen("tcp", ":0")
    if err != nil {
        t.Fatalf("listen: %v", err)
    }

    srv := grpc.NewServer()
    pb.RegisterUserServiceServer(srv, NewUserServiceServer(
        slog.New(slog.NewTextHandler(os.Stderr, nil)),
    ))

    go srv.Serve(lis)
    defer srv.Stop()

    // Connect a real client
    conn, err := grpc.NewClient(
        lis.Addr().String(),
        grpc.WithTransportCredentials(insecure.NewCredentials()),
    )
    if err != nil {
        t.Fatalf("dial: %v", err)
    }
    defer conn.Close()

    client := pb.NewUserServiceClient(conn)

    resp, err := client.GetUser(context.Background(), &pb.GetUserRequest{UserId: "user-1"})
    if err != nil {
        t.Fatalf("GetUser: %v", err)
    }

    if resp.User.Email != "alice@example.com" {
        t.Errorf("unexpected email: %s", resp.User.Email)
    }
}
```

---

## Part 8: Key Concepts Summary

### gRPC vs HTTP/REST — when to choose gRPC

| Factor | gRPC | REST |
|---|---|---|
| Protocol | HTTP/2 + binary | HTTP/1.1 or 2 + text |
| Schema | Strict (proto) | Optional (OpenAPI) |
| Performance | ~10x faster for small messages | Slower but universal |
| Streaming | Built-in bidirectional | Limited |
| Browser support | Via grpc-web proxy | Native |
| Use for | Internal service-to-service | Public APIs, browsers |

### Kafka Delivery Semantics

| Guarantee | How to achieve | Trade-off |
|---|---|---|
| At most once | Commit before processing | Can miss messages on crash |
| At least once | Commit after processing (our approach) | May reprocess duplicates |
| Exactly once | Kafka transactions + idempotent producers | Complex, slight overhead |

**Golden rule:** Design consumers to be idempotent. Use a deduplication key (like `order_id`) and check if you've already processed an event before acting on it.

### The Outbox Pattern (production recommendation)

Instead of writing to Kafka directly from the order-service:

1. Write the order AND an outbox record to the DB in one transaction
2. A separate outbox processor reads new records and publishes to Kafka
3. On success: mark the outbox record as published

This guarantees no message loss even if Kafka is temporarily unavailable.

```go
// Pseudocode — transactional outbox
func (s *OrderService) CreateOrder(ctx context.Context, req OrderRequest) error {
    return s.db.Transaction(ctx, func(tx *sql.Tx) error {
        // 1. Insert order
        if err := insertOrder(tx, req); err != nil {
            return err
        }
        // 2. Insert outbox record (same transaction)
        event := OrderEvent{...}
        payload, _ := json.Marshal(event)
        return insertOutbox(tx, "order-events", req.OrderID, payload)
        // If this transaction commits, the event WILL eventually be published.
        // If it rolls back, no event is published.
    })
}
```
