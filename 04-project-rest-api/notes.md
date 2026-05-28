# Chapter 4: Building a Production-Ready REST API in Go

You've learned Go's syntax, concurrency model, and standard library. Now we build something real. This chapter constructs a complete Task Management API from scratch — the kind of thing you'd actually ship.

By the end you will have a working API with JWT authentication, PostgreSQL persistence, structured logging, rate limiting, graceful shutdown, Docker packaging, and a test suite. Every line of code is here. Nothing is left as an exercise.

---

## Table of Contents

1. [Why this architecture?](#1-why-this-architecture)
2. [Project setup](#2-project-setup)
3. [Configuration](#3-configuration)
4. [Database connection](#4-database-connection)
5. [Models](#5-models)
6. [Repository layer](#6-repository-layer)
7. [Service layer](#7-service-layer)
8. [Response helpers](#8-response-helpers)
9. [JWT package](#9-jwt-package)
10. [Middleware](#10-middleware)
11. [Handlers](#11-handlers)
12. [Router setup](#12-router-setup)
13. [Main — wiring it together](#13-main--wiring-it-together)
14. [Database migrations](#14-database-migrations)
15. [Docker](#15-docker)
16. [Testing](#16-testing)
17. [Running the project](#17-running-the-project)
18. [What to explore next](#18-what-to-explore-next)

---

## 1. Why this architecture?

Before writing any code, understand the folder split:

```
task-api/
├── cmd/api/main.go          ← entry point only
├── internal/                ← code private to this module
│   ├── config/
│   ├── database/
│   ├── middleware/
│   ├── handler/
│   ├── model/
│   ├── repository/
│   └── service/
├── pkg/                     ← code you'd share with other modules
│   ├── jwt/
│   └── response/
├── migrations/
├── go.mod
├── Dockerfile
└── docker-compose.yml
```

**`internal/`** is a Go compiler rule: nothing outside this module can import packages under `internal/`. That gives you a hard boundary. Your handlers, models, and business logic stay private. If you later extract a separate CLI tool or a worker process as a separate module, it cannot accidentally depend on internal wiring.

**`pkg/`** holds things that are genuinely reusable — the JWT helper and response formatter have no business-domain knowledge, so they could live in a shared library someday.

**`cmd/api/`** contains only the wiring: create config, connect to DB, build router, start server. No business logic lives here.

**Layers and why they matter:**

```
Handler  →  receives HTTP, validates input shape, calls Service
Service  →  owns business rules, calls Repository
Repository → speaks SQL, returns domain models
```

Each layer depends only on the one below it, and only through interfaces. This makes testing straightforward: you swap the real PostgreSQL repository for an in-memory fake when testing the service layer, and swap the real service for a mock when testing handlers.

---

## 2. Project setup

```bash
mkdir task-api && cd task-api
go mod init github.com/yourname/task-api
```

Install dependencies:

```bash
go get github.com/gin-gonic/gin@v1.10.0
go get github.com/jackc/pgx/v5@v5.6.0
go get github.com/golang-jwt/jwt/v5@v5.2.1
go get github.com/joho/godotenv@v1.5.1
go get golang.org/x/crypto
go get github.com/stretchr/testify@v1.9.0
```

Create the folder tree:

```bash
mkdir -p cmd/api \
         internal/config \
         internal/database \
         internal/middleware \
         internal/handler \
         internal/model \
         internal/repository \
         internal/service \
         pkg/jwt \
         pkg/response \
         migrations
```

Create a `.env` file (never commit this):

```bash
# .env
APP_PORT=8080
APP_ENV=development

DB_HOST=localhost
DB_PORT=5432
DB_USER=taskuser
DB_PASSWORD=taskpass
DB_NAME=taskdb

JWT_SECRET=change-this-to-a-long-random-secret-in-production
JWT_EXPIRY_HOURS=24
```

---

## 3. Configuration

**`internal/config/config.go`**

```go
package config

import (
	"fmt"
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

// Config holds all application configuration loaded from environment variables.
// We use a plain struct — no reflection magic, no hidden behaviour.
type Config struct {
	AppPort string
	AppEnv  string

	DBHost     string
	DBPort     string
	DBUser     string
	DBPassword string
	DBName     string
	DBDSN      string // assembled from the fields above

	JWTSecret      string
	JWTExpiryHours int
}

// Load reads .env (if present) and then reads environment variables.
// Environment variables already set in the shell always win over the .env file,
// which is the standard twelve-factor behaviour.
func Load() (*Config, error) {
	// godotenv.Load does not overwrite existing env vars, so this is safe.
	// If the file does not exist (e.g. in a container) that is also fine.
	_ = godotenv.Load()

	cfg := &Config{
		AppPort:    getEnv("APP_PORT", "8080"),
		AppEnv:     getEnv("APP_ENV", "development"),
		DBHost:     getEnv("DB_HOST", "localhost"),
		DBPort:     getEnv("DB_PORT", "5432"),
		DBUser:     getEnv("DB_USER", "taskuser"),
		DBPassword: getEnv("DB_PASSWORD", ""),
		DBName:     getEnv("DB_NAME", "taskdb"),
		JWTSecret:  getEnv("JWT_SECRET", ""),
	}

	var err error
	cfg.JWTExpiryHours, err = strconv.Atoi(getEnv("JWT_EXPIRY_HOURS", "24"))
	if err != nil {
		return nil, fmt.Errorf("config: JWT_EXPIRY_HOURS must be an integer: %w", err)
	}

	// Assemble the DSN so callers don't have to construct it themselves.
	cfg.DBDSN = fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		cfg.DBHost, cfg.DBPort, cfg.DBUser, cfg.DBPassword, cfg.DBName,
	)

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// validate returns an error if any required field is empty.
func (c *Config) validate() error {
	required := map[string]string{
		"DB_PASSWORD": c.DBPassword,
		"JWT_SECRET":  c.JWTSecret,
	}
	for key, val := range required {
		if val == "" {
			return fmt.Errorf("config: required environment variable %s is not set", key)
		}
	}
	return nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
```

**Why a plain struct?** Many projects use libraries like `viper` or `envconfig`. They are fine, but they add indirection. A plain struct is explicit — every field is visible, validation is a regular function, and there is nothing to learn.

---

## 4. Database connection

**`internal/database/postgres.go`**

```go
package database

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DB wraps pgxpool.Pool so the rest of the codebase imports this package
// rather than pgx directly. This makes it easy to add instrumentation later.
type DB struct {
	Pool *pgxpool.Pool
}

// New creates a connection pool, pings the database, and returns the wrapper.
// Call Close() when the application shuts down.
func New(dsn string) (*DB, error) {
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("database: failed to parse config: %w", err)
	}

	// Connection pool tuning — adjust these for your workload.
	config.MaxConns = 25
	config.MinConns = 5
	config.MaxConnLifetime = 1 * time.Hour
	config.MaxConnIdleTime = 30 * time.Minute
	config.HealthCheckPeriod = 1 * time.Minute

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("database: failed to create pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("database: ping failed: %w", err)
	}

	return &DB{Pool: pool}, nil
}

// Close drains the pool. Safe to call multiple times.
func (db *DB) Close() {
	if db.Pool != nil {
		db.Pool.Close()
	}
}

// HealthCheck runs a lightweight query to verify the connection is alive.
// Used by a /health endpoint.
func (db *DB) HealthCheck(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	var result int
	err := db.Pool.QueryRow(ctx, "SELECT 1").Scan(&result)
	if err != nil {
		return fmt.Errorf("database: health check failed: %w", err)
	}
	return nil
}
```

**`pgxpool` vs `database/sql`:** pgx's native pool is significantly faster than going through `database/sql`, and its row scanning API is cleaner. The tradeoff is that you are coupled to PostgreSQL. For this project, that is fine — we are explicitly building a Postgres-backed service.

---

## 5. Models

**`internal/model/task.go`**

```go
package model

import (
	"time"
)

// TaskStatus enumerates the allowed values for a task's status.
// Using a named string type instead of plain strings catches typos at compile time.
type TaskStatus string

const (
	StatusTodo       TaskStatus = "todo"
	StatusInProgress TaskStatus = "in_progress"
	StatusDone       TaskStatus = "done"
)

// IsValid reports whether the status value is one of the defined constants.
func (s TaskStatus) IsValid() bool {
	switch s {
	case StatusTodo, StatusInProgress, StatusDone:
		return true
	}
	return false
}

// Task is the core domain object.
// The `db` tags are used by pgx for scanning.
// The `json` tags control the API representation.
type Task struct {
	ID          int64      `db:"id"           json:"id"`
	UserID      int64      `db:"user_id"      json:"user_id"`
	Title       string     `db:"title"        json:"title"`
	Description string     `db:"description"  json:"description"`
	Status      TaskStatus `db:"status"       json:"status"`
	CreatedAt   time.Time  `db:"created_at"   json:"created_at"`
	UpdatedAt   time.Time  `db:"updated_at"   json:"updated_at"`
}

// User is the authentication domain object.
// Password is never included in JSON responses — note the `json:"-"` tag.
type User struct {
	ID           int64     `db:"id"            json:"id"`
	Email        string    `db:"email"         json:"email"`
	PasswordHash string    `db:"password_hash" json:"-"`
	CreatedAt    time.Time `db:"created_at"    json:"created_at"`
}

// CreateTaskRequest is what the handler reads from the request body.
// Validate() is called in the service layer, not the handler, so business
// rules are not spread across multiple places.
type CreateTaskRequest struct {
	Title       string     `json:"title"       binding:"required,min=1,max=255"`
	Description string     `json:"description" binding:"max=1000"`
	Status      TaskStatus `json:"status"`
}

// UpdateTaskRequest allows partial updates — every field is a pointer so we
// can distinguish "not provided" from "set to zero value".
type UpdateTaskRequest struct {
	Title       *string     `json:"title"       binding:"omitempty,min=1,max=255"`
	Description *string     `json:"description" binding:"omitempty,max=1000"`
	Status      *TaskStatus `json:"status"      binding:"omitempty"`
}

// RegisterRequest is the payload for POST /auth/register.
type RegisterRequest struct {
	Email    string `json:"email"    binding:"required,email"`
	Password string `json:"password" binding:"required,min=8"`
}

// LoginRequest is the payload for POST /auth/login.
type LoginRequest struct {
	Email    string `json:"email"    binding:"required,email"`
	Password string `json:"password" binding:"required"`
}

// AuthResponse is returned after a successful login or register.
type AuthResponse struct {
	Token string `json:"token"`
	User  *User  `json:"user"`
}

// PaginationParams are read from query strings on list endpoints.
type PaginationParams struct {
	Page    int `form:"page"     binding:"omitempty,min=1"`
	PerPage int `form:"per_page" binding:"omitempty,min=1,max=100"`
}

// Normalize sets defaults for omitted fields.
func (p *PaginationParams) Normalize() {
	if p.Page == 0 {
		p.Page = 1
	}
	if p.PerPage == 0 {
		p.PerPage = 20
	}
}

// Offset returns the SQL OFFSET value for a given page.
func (p *PaginationParams) Offset() int {
	return (p.Page - 1) * p.PerPage
}

// ListTasksResponse wraps a page of tasks with pagination metadata.
type ListTasksResponse struct {
	Tasks   []*Task `json:"tasks"`
	Total   int64   `json:"total"`
	Page    int     `json:"page"`
	PerPage int     `json:"per_page"`
}
```

---

## 6. Repository layer

The repository layer owns all SQL. Nothing above it writes a query string.

**`internal/repository/task_repo.go`**

```go
package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yourname/task-api/internal/model"
)

// ErrNotFound is returned when a query matches no rows.
// Callers compare against this sentinel rather than pgx.ErrNoRows so they
// are not coupled to the database driver.
var ErrNotFound = errors.New("record not found")

// TaskRepository defines the interface that the service layer depends on.
// Having an interface here is what makes the service testable without a real database.
type TaskRepository interface {
	Create(ctx context.Context, task *model.Task) (*model.Task, error)
	GetByID(ctx context.Context, id, userID int64) (*model.Task, error)
	List(ctx context.Context, userID int64, params model.PaginationParams) ([]*model.Task, int64, error)
	Update(ctx context.Context, task *model.Task) (*model.Task, error)
	Delete(ctx context.Context, id, userID int64) error
}

// UserRepository defines the interface for user persistence.
type UserRepository interface {
	Create(ctx context.Context, user *model.User) (*model.User, error)
	GetByEmail(ctx context.Context, email string) (*model.User, error)
	GetByID(ctx context.Context, id int64) (*model.User, error)
}

// pgTaskRepository is the PostgreSQL implementation.
// It is unexported — callers use the TaskRepository interface.
type pgTaskRepository struct {
	pool *pgxpool.Pool
}

// NewTaskRepository returns a PostgreSQL-backed TaskRepository.
func NewTaskRepository(pool *pgxpool.Pool) TaskRepository {
	return &pgTaskRepository{pool: pool}
}

func (r *pgTaskRepository) Create(ctx context.Context, task *model.Task) (*model.Task, error) {
	query := `
		INSERT INTO tasks (user_id, title, description, status)
		VALUES ($1, $2, $3, $4)
		RETURNING id, user_id, title, description, status, created_at, updated_at
	`
	row := r.pool.QueryRow(ctx, query,
		task.UserID,
		task.Title,
		task.Description,
		task.Status,
	)

	var created model.Task
	err := row.Scan(
		&created.ID,
		&created.UserID,
		&created.Title,
		&created.Description,
		&created.Status,
		&created.CreatedAt,
		&created.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("repository: create task: %w", err)
	}
	return &created, nil
}

func (r *pgTaskRepository) GetByID(ctx context.Context, id, userID int64) (*model.Task, error) {
	query := `
		SELECT id, user_id, title, description, status, created_at, updated_at
		FROM tasks
		WHERE id = $1 AND user_id = $2
	`
	row := r.pool.QueryRow(ctx, query, id, userID)

	var task model.Task
	err := row.Scan(
		&task.ID,
		&task.UserID,
		&task.Title,
		&task.Description,
		&task.Status,
		&task.CreatedAt,
		&task.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("repository: get task by id: %w", err)
	}
	return &task, nil
}

func (r *pgTaskRepository) List(ctx context.Context, userID int64, params model.PaginationParams) ([]*model.Task, int64, error) {
	// Run count and data queries in parallel using a transaction for consistency.
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("repository: list tasks begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck — rollback on read-only tx is fine

	var total int64
	err = tx.QueryRow(ctx,
		"SELECT COUNT(*) FROM tasks WHERE user_id = $1", userID,
	).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("repository: list tasks count: %w", err)
	}

	query := `
		SELECT id, user_id, title, description, status, created_at, updated_at
		FROM tasks
		WHERE user_id = $1
		ORDER BY created_at DESC
		LIMIT $2 OFFSET $3
	`
	rows, err := tx.Query(ctx, query, userID, params.PerPage, params.Offset())
	if err != nil {
		return nil, 0, fmt.Errorf("repository: list tasks query: %w", err)
	}
	defer rows.Close()

	var tasks []*model.Task
	for rows.Next() {
		var t model.Task
		if err := rows.Scan(
			&t.ID, &t.UserID, &t.Title, &t.Description,
			&t.Status, &t.CreatedAt, &t.UpdatedAt,
		); err != nil {
			return nil, 0, fmt.Errorf("repository: list tasks scan: %w", err)
		}
		tasks = append(tasks, &t)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("repository: list tasks rows: %w", err)
	}

	return tasks, total, nil
}

func (r *pgTaskRepository) Update(ctx context.Context, task *model.Task) (*model.Task, error) {
	query := `
		UPDATE tasks
		SET title = $1, description = $2, status = $3, updated_at = NOW()
		WHERE id = $4 AND user_id = $5
		RETURNING id, user_id, title, description, status, created_at, updated_at
	`
	row := r.pool.QueryRow(ctx, query,
		task.Title,
		task.Description,
		task.Status,
		task.ID,
		task.UserID,
	)

	var updated model.Task
	err := row.Scan(
		&updated.ID, &updated.UserID, &updated.Title, &updated.Description,
		&updated.Status, &updated.CreatedAt, &updated.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("repository: update task: %w", err)
	}
	return &updated, nil
}

func (r *pgTaskRepository) Delete(ctx context.Context, id, userID int64) error {
	result, err := r.pool.Exec(ctx,
		"DELETE FROM tasks WHERE id = $1 AND user_id = $2", id, userID,
	)
	if err != nil {
		return fmt.Errorf("repository: delete task: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// --- User repository ---

type pgUserRepository struct {
	pool *pgxpool.Pool
}

// NewUserRepository returns a PostgreSQL-backed UserRepository.
func NewUserRepository(pool *pgxpool.Pool) UserRepository {
	return &pgUserRepository{pool: pool}
}

func (r *pgUserRepository) Create(ctx context.Context, user *model.User) (*model.User, error) {
	query := `
		INSERT INTO users (email, password_hash)
		VALUES ($1, $2)
		RETURNING id, email, password_hash, created_at
	`
	row := r.pool.QueryRow(ctx, query, user.Email, user.PasswordHash)

	var created model.User
	err := row.Scan(&created.ID, &created.Email, &created.PasswordHash, &created.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("repository: create user: %w", err)
	}
	return &created, nil
}

func (r *pgUserRepository) GetByEmail(ctx context.Context, email string) (*model.User, error) {
	query := `SELECT id, email, password_hash, created_at FROM users WHERE email = $1`
	row := r.pool.QueryRow(ctx, query, email)

	var user model.User
	err := row.Scan(&user.ID, &user.Email, &user.PasswordHash, &user.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("repository: get user by email: %w", err)
	}
	return &user, nil
}

func (r *pgUserRepository) GetByID(ctx context.Context, id int64) (*model.User, error) {
	query := `SELECT id, email, password_hash, created_at FROM users WHERE id = $1`
	row := r.pool.QueryRow(ctx, query, id)

	var user model.User
	err := row.Scan(&user.ID, &user.Email, &user.PasswordHash, &user.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("repository: get user by id: %w", err)
	}
	return &user, nil
}
```

**Key decisions:**
- `GetByID` takes both `id` and `userID`. This prevents a user from reading another user's tasks — it is an authorization check baked into the query.
- `List` uses a transaction so the count and the page of data are consistent. Without a transaction, a concurrent insert could make the numbers disagree.
- Errors from pgx are wrapped with context before being returned, so when an error surfaces in a log it reads `repository: list tasks scan: ...` rather than a bare pgx message.

---

## 7. Service layer

**`internal/service/task_service.go`**

```go
package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/yourname/task-api/internal/model"
	"github.com/yourname/task-api/internal/repository"
)

// AppError wraps a user-facing message and an HTTP status code together.
// The service layer creates these; the handler layer reads them.
type AppError struct {
	Code    int    // HTTP status code
	Message string // Safe to show to the caller
	Err     error  // Underlying cause, logged but not exposed
}

func (e *AppError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Err)
	}
	return e.Message
}

func (e *AppError) Unwrap() error {
	return e.Err
}

// newAppError is a convenience constructor.
func newAppError(code int, message string, cause error) *AppError {
	return &AppError{Code: code, Message: message, Err: cause}
}

// TaskService defines the business operations available to handlers.
type TaskService interface {
	Create(ctx context.Context, userID int64, req *model.CreateTaskRequest) (*model.Task, error)
	GetByID(ctx context.Context, userID, taskID int64) (*model.Task, error)
	List(ctx context.Context, userID int64, params model.PaginationParams) (*model.ListTasksResponse, error)
	Update(ctx context.Context, userID, taskID int64, req *model.UpdateTaskRequest) (*model.Task, error)
	Delete(ctx context.Context, userID, taskID int64) error
}

type taskService struct {
	repo repository.TaskRepository
}

// NewTaskService wires the service to a repository.
func NewTaskService(repo repository.TaskRepository) TaskService {
	return &taskService{repo: repo}
}

func (s *taskService) Create(ctx context.Context, userID int64, req *model.CreateTaskRequest) (*model.Task, error) {
	// Apply default status if caller did not provide one.
	status := req.Status
	if status == "" {
		status = model.StatusTodo
	}
	if !status.IsValid() {
		return nil, newAppError(400, fmt.Sprintf("invalid status %q; must be todo, in_progress, or done", status), nil)
	}

	task := &model.Task{
		UserID:      userID,
		Title:       req.Title,
		Description: req.Description,
		Status:      status,
	}

	created, err := s.repo.Create(ctx, task)
	if err != nil {
		return nil, newAppError(500, "failed to create task", err)
	}
	return created, nil
}

func (s *taskService) GetByID(ctx context.Context, userID, taskID int64) (*model.Task, error) {
	task, err := s.repo.GetByID(ctx, taskID, userID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, newAppError(404, "task not found", err)
		}
		return nil, newAppError(500, "failed to retrieve task", err)
	}
	return task, nil
}

func (s *taskService) List(ctx context.Context, userID int64, params model.PaginationParams) (*model.ListTasksResponse, error) {
	params.Normalize()

	tasks, total, err := s.repo.List(ctx, userID, params)
	if err != nil {
		return nil, newAppError(500, "failed to list tasks", err)
	}

	// Ensure we return an empty slice rather than null in JSON.
	if tasks == nil {
		tasks = []*model.Task{}
	}

	return &model.ListTasksResponse{
		Tasks:   tasks,
		Total:   total,
		Page:    params.Page,
		PerPage: params.PerPage,
	}, nil
}

func (s *taskService) Update(ctx context.Context, userID, taskID int64, req *model.UpdateTaskRequest) (*model.Task, error) {
	// Fetch existing task to apply partial update.
	existing, err := s.repo.GetByID(ctx, taskID, userID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, newAppError(404, "task not found", err)
		}
		return nil, newAppError(500, "failed to retrieve task for update", err)
	}

	// Apply only the fields that were provided.
	if req.Title != nil {
		existing.Title = *req.Title
	}
	if req.Description != nil {
		existing.Description = *req.Description
	}
	if req.Status != nil {
		if !req.Status.IsValid() {
			return nil, newAppError(400, fmt.Sprintf("invalid status %q", *req.Status), nil)
		}
		existing.Status = *req.Status
	}

	updated, err := s.repo.Update(ctx, existing)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, newAppError(404, "task not found", err)
		}
		return nil, newAppError(500, "failed to update task", err)
	}
	return updated, nil
}

func (s *taskService) Delete(ctx context.Context, userID, taskID int64) error {
	err := s.repo.Delete(ctx, taskID, userID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return newAppError(404, "task not found", err)
		}
		return newAppError(500, "failed to delete task", err)
	}
	return nil
}
```

**`internal/service/auth_service.go`**

```go
package service

import (
	"context"
	"errors"
	"fmt"

	"golang.org/x/crypto/bcrypt"
	"github.com/yourname/task-api/internal/model"
	"github.com/yourname/task-api/internal/repository"
	jwtpkg "github.com/yourname/task-api/pkg/jwt"
)

// AuthService handles registration and login.
type AuthService interface {
	Register(ctx context.Context, req *model.RegisterRequest) (*model.AuthResponse, error)
	Login(ctx context.Context, req *model.LoginRequest) (*model.AuthResponse, error)
}

type authService struct {
	userRepo  repository.UserRepository
	jwtHelper *jwtpkg.Helper
}

// NewAuthService creates an AuthService.
func NewAuthService(userRepo repository.UserRepository, jwtHelper *jwtpkg.Helper) AuthService {
	return &authService{userRepo: userRepo, jwtHelper: jwtHelper}
}

func (s *authService) Register(ctx context.Context, req *model.RegisterRequest) (*model.AuthResponse, error) {
	// Check if email is already taken.
	existing, err := s.userRepo.GetByEmail(ctx, req.Email)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return nil, newAppError(500, "failed to check existing user", err)
	}
	if existing != nil {
		return nil, newAppError(409, "email is already registered", nil)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, newAppError(500, "failed to hash password", err)
	}

	user, err := s.userRepo.Create(ctx, &model.User{
		Email:        req.Email,
		PasswordHash: string(hash),
	})
	if err != nil {
		return nil, newAppError(500, "failed to create user", err)
	}

	token, err := s.jwtHelper.Generate(user.ID)
	if err != nil {
		return nil, newAppError(500, "failed to generate token", err)
	}

	return &model.AuthResponse{Token: token, User: user}, nil
}

func (s *authService) Login(ctx context.Context, req *model.LoginRequest) (*model.AuthResponse, error) {
	user, err := s.userRepo.GetByEmail(ctx, req.Email)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			// Return the same message for wrong email and wrong password
			// to avoid leaking which accounts exist.
			return nil, newAppError(401, "invalid credentials", nil)
		}
		return nil, newAppError(500, "failed to look up user", err)
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		return nil, newAppError(401, "invalid credentials", fmt.Errorf("bcrypt mismatch: %w", err))
	}

	token, err := s.jwtHelper.Generate(user.ID)
	if err != nil {
		return nil, newAppError(500, "failed to generate token", err)
	}

	return &model.AuthResponse{Token: token, User: user}, nil
}
```

---

## 8. Response helpers

**`pkg/response/response.go`**

```go
package response

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Envelope is the consistent JSON shape for every response.
//
//	{ "success": true,  "data": {...} }
//	{ "success": false, "error": "..." }
type Envelope struct {
	Success bool        `json:"success"`
	Data    interface{} `json:"data,omitempty"`
	Error   string      `json:"error,omitempty"`
}

// OK writes a 200 response with the given data payload.
func OK(c *gin.Context, data interface{}) {
	c.JSON(http.StatusOK, Envelope{Success: true, Data: data})
}

// Created writes a 201 response.
func Created(c *gin.Context, data interface{}) {
	c.JSON(http.StatusCreated, Envelope{Success: true, Data: data})
}

// NoContent writes a 204 response (no body).
func NoContent(c *gin.Context) {
	c.Status(http.StatusNoContent)
}

// Error writes a JSON error response using the given status code.
func Error(c *gin.Context, statusCode int, message string) {
	c.JSON(statusCode, Envelope{Success: false, Error: message})
}

// BadRequest writes a 400 response.
func BadRequest(c *gin.Context, message string) {
	Error(c, http.StatusBadRequest, message)
}

// Unauthorized writes a 401 response.
func Unauthorized(c *gin.Context, message string) {
	Error(c, http.StatusUnauthorized, message)
}

// Forbidden writes a 403 response.
func Forbidden(c *gin.Context, message string) {
	Error(c, http.StatusForbidden, message)
}

// NotFound writes a 404 response.
func NotFound(c *gin.Context, message string) {
	Error(c, http.StatusNotFound, message)
}

// InternalError writes a 500 response.
func InternalError(c *gin.Context) {
	Error(c, http.StatusInternalServerError, "an unexpected error occurred")
}
```

---

## 9. JWT package

**`pkg/jwt/jwt.go`**

```go
package jwt

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims embeds the registered JWT claims and adds our custom field.
type Claims struct {
	UserID int64 `json:"user_id"`
	jwt.RegisteredClaims
}

// Helper holds the signing key and expiry duration.
type Helper struct {
	secret      []byte
	expiryHours int
}

// NewHelper creates a Helper.
func NewHelper(secret string, expiryHours int) *Helper {
	return &Helper{
		secret:      []byte(secret),
		expiryHours: expiryHours,
	}
}

// Generate signs a new token for the given user ID.
func (h *Helper) Generate(userID int64) (string, error) {
	now := time.Now()
	claims := Claims{
		UserID: userID,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Duration(h.expiryHours) * time.Hour)),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(h.secret)
	if err != nil {
		return "", fmt.Errorf("jwt: sign token: %w", err)
	}
	return signed, nil
}

// Validate parses and validates a token string, returning the claims on success.
func (h *Helper) Validate(tokenString string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(t *jwt.Token) (interface{}, error) {
		// Ensure the algorithm is what we expect — prevents algorithm confusion attacks.
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("jwt: unexpected signing method: %v", t.Header["alg"])
		}
		return h.secret, nil
	})

	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, fmt.Errorf("jwt: token expired")
		}
		return nil, fmt.Errorf("jwt: invalid token: %w", err)
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("jwt: invalid claims")
	}
	return claims, nil
}
```

**Why we check the signing method:** The JWT spec allows an `alg: none` header. If you skip the algorithm check, an attacker can strip the signature and forge tokens. Always verify that the method is the one you expect.

---

## 10. Middleware

**`internal/middleware/auth.go`**

```go
package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"
	jwtpkg "github.com/yourname/task-api/pkg/jwt"
	"github.com/yourname/task-api/pkg/response"
)

// ContextKeyUserID is the key used to store the authenticated user's ID
// in gin.Context. Using a typed constant avoids key collisions.
const ContextKeyUserID = "user_id"

// Auth returns a Gin middleware that validates a Bearer token and stores the
// user ID in the context. Routes behind this middleware can call UserIDFromContext.
func Auth(jwtHelper *jwtpkg.Helper) gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			response.Unauthorized(c, "authorization header is required")
			c.Abort()
			return
		}

		// Header format: "Bearer <token>"
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
			response.Unauthorized(c, "authorization header must be in format: Bearer <token>")
			c.Abort()
			return
		}

		claims, err := jwtHelper.Validate(parts[1])
		if err != nil {
			response.Unauthorized(c, "invalid or expired token")
			c.Abort()
			return
		}

		c.Set(ContextKeyUserID, claims.UserID)
		c.Next()
	}
}

// UserIDFromContext extracts the authenticated user's ID from the Gin context.
// Panics if the middleware was not applied — this is intentional, as it is a
// programming error to call this on an unprotected route.
func UserIDFromContext(c *gin.Context) int64 {
	id, exists := c.Get(ContextKeyUserID)
	if !exists {
		panic("middleware.Auth was not applied to this route")
	}
	return id.(int64)
}
```

**`internal/middleware/logger.go`**

```go
package middleware

import (
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
)

// Logger returns a Gin middleware that logs each request using slog.
// slog is in the standard library as of Go 1.21.
func Logger(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		query := c.Request.URL.RawQuery

		c.Next() // process the request

		duration := time.Since(start)
		statusCode := c.Writer.Status()

		logFn := logger.Info
		if statusCode >= 500 {
			logFn = logger.Error
		} else if statusCode >= 400 {
			logFn = logger.Warn
		}

		attrs := []any{
			"method", c.Request.Method,
			"path", path,
			"status", statusCode,
			"duration_ms", duration.Milliseconds(),
			"ip", c.ClientIP(),
		}
		if query != "" {
			attrs = append(attrs, "query", query)
		}
		if len(c.Errors) > 0 {
			attrs = append(attrs, "errors", c.Errors.String())
		}

		logFn("request", attrs...)
	}
}
```

**`internal/middleware/cors.go`**

```go
package middleware

import (
	"github.com/gin-gonic/gin"
)

// CORS returns a simple CORS middleware.
// In production you should restrict AllowOrigins to your actual frontend domain.
func CORS() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Origin, Content-Type, Authorization")
		c.Header("Access-Control-Max-Age", "86400")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	}
}
```

**`internal/middleware/ratelimit.go`**

```go
package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/yourname/task-api/pkg/response"
)

// tokenBucket is a simple per-IP token bucket rate limiter.
// For production, replace this with a Redis-backed limiter so it works
// across multiple instances.
type tokenBucket struct {
	mu       sync.Mutex
	tokens   float64
	capacity float64
	refillPS float64 // tokens added per second
	lastSeen time.Time
}

func (tb *tokenBucket) allow() bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(tb.lastSeen).Seconds()
	tb.lastSeen = now

	tb.tokens += elapsed * tb.refillPS
	if tb.tokens > tb.capacity {
		tb.tokens = tb.capacity
	}

	if tb.tokens >= 1 {
		tb.tokens--
		return true
	}
	return false
}

// RateLimiter returns a middleware that allows at most `rps` requests per second
// per IP address, with a burst capacity of `burst`.
func RateLimiter(rps float64, burst float64) gin.HandlerFunc {
	var mu sync.Mutex
	buckets := make(map[string]*tokenBucket)

	// Periodically clean up buckets for IPs that have not been seen in a while.
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			mu.Lock()
			cutoff := time.Now().Add(-10 * time.Minute)
			for ip, b := range buckets {
				b.mu.Lock()
				if b.lastSeen.Before(cutoff) {
					delete(buckets, ip)
				}
				b.mu.Unlock()
			}
			mu.Unlock()
		}
	}()

	return func(c *gin.Context) {
		ip := c.ClientIP()

		mu.Lock()
		b, exists := buckets[ip]
		if !exists {
			b = &tokenBucket{
				tokens:   burst,
				capacity: burst,
				refillPS: rps,
				lastSeen: time.Now(),
			}
			buckets[ip] = b
		}
		mu.Unlock()

		if !b.allow() {
			c.Header("Retry-After", "1")
			response.Error(c, http.StatusTooManyRequests, "rate limit exceeded")
			c.Abort()
			return
		}

		c.Next()
	}
}
```

---

## 11. Handlers

**`internal/handler/task.go`**

```go
package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/yourname/task-api/internal/middleware"
	"github.com/yourname/task-api/internal/model"
	"github.com/yourname/task-api/internal/service"
	"github.com/yourname/task-api/pkg/response"
)

// TaskHandler holds the dependencies for task-related route handlers.
type TaskHandler struct {
	taskSvc service.TaskService
}

// NewTaskHandler creates a TaskHandler.
func NewTaskHandler(taskSvc service.TaskService) *TaskHandler {
	return &TaskHandler{taskSvc: taskSvc}
}

// handleServiceError maps service.AppError to the correct HTTP response.
// All handlers call this for any error returned from the service layer.
func handleServiceError(c *gin.Context, err error) {
	var appErr *service.AppError
	if errors.As(err, &appErr) {
		response.Error(c, appErr.Code, appErr.Message)
		return
	}
	response.InternalError(c)
}

// Create handles POST /api/v1/tasks
func (h *TaskHandler) Create(c *gin.Context) {
	userID := middleware.UserIDFromContext(c)

	var req model.CreateTaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request body: "+err.Error())
		return
	}

	task, err := h.taskSvc.Create(c.Request.Context(), userID, &req)
	if err != nil {
		handleServiceError(c, err)
		return
	}

	response.Created(c, task)
}

// List handles GET /api/v1/tasks
func (h *TaskHandler) List(c *gin.Context) {
	userID := middleware.UserIDFromContext(c)

	var params model.PaginationParams
	if err := c.ShouldBindQuery(&params); err != nil {
		response.BadRequest(c, "invalid query parameters: "+err.Error())
		return
	}

	result, err := h.taskSvc.List(c.Request.Context(), userID, params)
	if err != nil {
		handleServiceError(c, err)
		return
	}

	response.OK(c, result)
}

// GetByID handles GET /api/v1/tasks/:id
func (h *TaskHandler) GetByID(c *gin.Context) {
	userID := middleware.UserIDFromContext(c)

	taskID, err := parseIDParam(c, "id")
	if err != nil {
		return // parseIDParam already wrote the error response
	}

	task, err := h.taskSvc.GetByID(c.Request.Context(), userID, taskID)
	if err != nil {
		handleServiceError(c, err)
		return
	}

	response.OK(c, task)
}

// Update handles PUT /api/v1/tasks/:id
func (h *TaskHandler) Update(c *gin.Context) {
	userID := middleware.UserIDFromContext(c)

	taskID, err := parseIDParam(c, "id")
	if err != nil {
		return
	}

	var req model.UpdateTaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request body: "+err.Error())
		return
	}

	task, err := h.taskSvc.Update(c.Request.Context(), userID, taskID, &req)
	if err != nil {
		handleServiceError(c, err)
		return
	}

	response.OK(c, task)
}

// Delete handles DELETE /api/v1/tasks/:id
func (h *TaskHandler) Delete(c *gin.Context) {
	userID := middleware.UserIDFromContext(c)

	taskID, err := parseIDParam(c, "id")
	if err != nil {
		return
	}

	if err := h.taskSvc.Delete(c.Request.Context(), userID, taskID); err != nil {
		handleServiceError(c, err)
		return
	}

	response.NoContent(c)
}

// parseIDParam reads a path parameter as int64, writing a 400 response on failure.
func parseIDParam(c *gin.Context, name string) (int64, error) {
	raw := c.Param(name)
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id < 1 {
		response.BadRequest(c, name+" must be a positive integer")
		return 0, err
	}
	return id, nil
}

// AuthHandler handles authentication routes.
type AuthHandler struct {
	authSvc service.AuthService
}

// NewAuthHandler creates an AuthHandler.
func NewAuthHandler(authSvc service.AuthService) *AuthHandler {
	return &AuthHandler{authSvc: authSvc}
}

// Register handles POST /api/v1/auth/register
func (h *AuthHandler) Register(c *gin.Context) {
	var req model.RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request body: "+err.Error())
		return
	}

	result, err := h.authSvc.Register(c.Request.Context(), &req)
	if err != nil {
		handleServiceError(c, err)
		return
	}

	c.JSON(http.StatusCreated, result)
}

// Login handles POST /api/v1/auth/login
func (h *AuthHandler) Login(c *gin.Context) {
	var req model.LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request body: "+err.Error())
		return
	}

	result, err := h.authSvc.Login(c.Request.Context(), &req)
	if err != nil {
		handleServiceError(c, err)
		return
	}

	response.OK(c, result)
}
```

**`internal/handler/health.go`**

```go
package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// HealthChecker is implemented by the database wrapper.
type HealthChecker interface {
	HealthCheck(ctx context.Context) error
}

// HealthHandler provides a liveness probe endpoint.
type HealthHandler struct {
	db HealthChecker
}

// NewHealthHandler creates a HealthHandler.
func NewHealthHandler(db HealthChecker) *HealthHandler {
	return &HealthHandler{db: db}
}

// Check handles GET /health
func (h *HealthHandler) Check(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	if err := h.db.HealthCheck(ctx); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"status": "unhealthy",
			"error":  err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status": "healthy",
		"time":   time.Now().UTC(),
	})
}
```

---

## 12. Router setup

**`internal/router.go`** — put this directly in `internal/` since it orchestrates across packages.

```go
package internal

import (
	"log/slog"

	"github.com/gin-gonic/gin"
	"github.com/yourname/task-api/internal/handler"
	"github.com/yourname/task-api/internal/middleware"
	jwtpkg "github.com/yourname/task-api/pkg/jwt"
)

// RouterConfig groups all the handler dependencies.
type RouterConfig struct {
	TaskHandler   *handler.TaskHandler
	AuthHandler   *handler.AuthHandler
	HealthHandler *handler.HealthHandler
	JWTHelper     *jwtpkg.Helper
	Logger        *slog.Logger
}

// NewRouter builds and returns the Gin engine with all routes registered.
func NewRouter(cfg RouterConfig) *gin.Engine {
	r := gin.New() // gin.New() instead of gin.Default() so we control middleware

	// Global middleware — applied to every request.
	r.Use(middleware.Logger(cfg.Logger))
	r.Use(middleware.CORS())
	r.Use(middleware.RateLimiter(100, 20)) // 100 req/s, burst of 20
	r.Use(gin.Recovery())                  // recover from panics

	// Health check — no auth required.
	r.GET("/health", cfg.HealthHandler.Check)

	// API v1 route group.
	v1 := r.Group("/api/v1")

	// Auth routes — no JWT required.
	auth := v1.Group("/auth")
	{
		auth.POST("/register", cfg.AuthHandler.Register)
		auth.POST("/login", cfg.AuthHandler.Login)
	}

	// Protected routes — JWT required.
	tasks := v1.Group("/tasks")
	tasks.Use(middleware.Auth(cfg.JWTHelper))
	{
		tasks.GET("", cfg.TaskHandler.List)
		tasks.POST("", cfg.TaskHandler.Create)
		tasks.GET("/:id", cfg.TaskHandler.GetByID)
		tasks.PUT("/:id", cfg.TaskHandler.Update)
		tasks.DELETE("/:id", cfg.TaskHandler.Delete)
	}

	return r
}
```

---

## 13. Main — wiring it together

**`cmd/api/main.go`**

```go
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/yourname/task-api/internal"
	"github.com/yourname/task-api/internal/config"
	"github.com/yourname/task-api/internal/database"
	"github.com/yourname/task-api/internal/handler"
	"github.com/yourname/task-api/internal/repository"
	"github.com/yourname/task-api/internal/service"
	jwtpkg "github.com/yourname/task-api/pkg/jwt"
)

func main() {
	// --- Structured logger ---
	// In production, replace slog.NewTextHandler with slog.NewJSONHandler
	// so logs are machine-parseable.
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	// --- Configuration ---
	cfg, err := config.Load()
	if err != nil {
		logger.Error("failed to load config", "error", err)
		os.Exit(1)
	}
	logger.Info("config loaded", "env", cfg.AppEnv, "port", cfg.AppPort)

	// --- Database ---
	db, err := database.New(cfg.DBDSN)
	if err != nil {
		logger.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	logger.Info("database connected")

	// --- Dependencies ---
	jwtHelper := jwtpkg.NewHelper(cfg.JWTSecret, cfg.JWTExpiryHours)

	taskRepo := repository.NewTaskRepository(db.Pool)
	userRepo := repository.NewUserRepository(db.Pool)

	taskSvc := service.NewTaskService(taskRepo)
	authSvc := service.NewAuthService(userRepo, jwtHelper)

	taskH := handler.NewTaskHandler(taskSvc)
	authH := handler.NewAuthHandler(authSvc)
	healthH := handler.NewHealthHandler(db)

	// --- Router ---
	router := internal.NewRouter(internal.RouterConfig{
		TaskHandler:   taskH,
		AuthHandler:   authH,
		HealthHandler: healthH,
		JWTHelper:     jwtHelper,
		Logger:        logger,
	})

	// --- HTTP server ---
	srv := &http.Server{
		Addr:         ":" + cfg.AppPort,
		Handler:      router,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Start the server in a goroutine so it does not block the shutdown logic.
	go func() {
		logger.Info("server starting", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	// --- Graceful shutdown ---
	// Block here until we receive SIGINT or SIGTERM (Ctrl+C or `kill`).
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit
	logger.Info("shutdown signal received", "signal", sig)

	// Give in-flight requests 30 seconds to complete.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		logger.Error("server shutdown error", "error", err)
		os.Exit(1)
	}

	logger.Info("server shutdown complete")
}
```

**Graceful shutdown walkthrough:**

1. `signal.Notify` registers our channel to receive OS signals.
2. The `<-quit` receive blocks until a signal arrives.
3. `srv.Shutdown(ctx)` stops accepting new connections and waits for active requests to finish — up to 30 seconds.
4. `db.Close()` is called by `defer` after Shutdown returns, closing the pool after all handlers are done.

This means a `Ctrl+C` during a slow database query will not drop the response. Kubernetes `SIGTERM` during a rolling deploy will let the pod finish its work before terminating.

---

## 14. Database migrations

For a production project you would use a migration tool (golang-migrate, goose, atlas). Here we keep it simple with plain SQL files.

**`migrations/001_create_tasks.sql`**

```sql
-- Run this against your database before starting the server.
-- For automated migrations, pipe this through golang-migrate or goose.

CREATE TABLE IF NOT EXISTS users (
    id            BIGSERIAL PRIMARY KEY,
    email         TEXT      NOT NULL UNIQUE,
    password_hash TEXT      NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_users_email ON users (email);

CREATE TABLE IF NOT EXISTS tasks (
    id          BIGSERIAL PRIMARY KEY,
    user_id     BIGINT      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title       TEXT        NOT NULL CHECK (length(title) BETWEEN 1 AND 255),
    description TEXT        NOT NULL DEFAULT '',
    status      TEXT        NOT NULL DEFAULT 'todo'
                            CHECK (status IN ('todo', 'in_progress', 'done')),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_tasks_user_id ON tasks (user_id);
CREATE INDEX IF NOT EXISTS idx_tasks_created_at ON tasks (created_at DESC);
```

Apply it:

```bash
psql -h localhost -U taskuser -d taskdb -f migrations/001_create_tasks.sql
```

---

## 15. Docker

**`Dockerfile`**

```dockerfile
# ---- Stage 1: build ----
FROM golang:1.23-alpine AS builder

# Install ca-certificates for HTTPS calls if your app makes any.
RUN apk add --no-cache ca-certificates git

WORKDIR /app

# Copy go.mod and go.sum first to leverage Docker layer caching.
# If these files do not change, the go mod download step is cached.
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-w -s" -o /app/server ./cmd/api

# ---- Stage 2: run ----
# Use a minimal base image. The final image has no Go toolchain, no shell,
# nothing except the binary and TLS certificates.
FROM gcr.io/distroless/static-debian12

COPY --from=builder /app/server /server

# Run as non-root for security.
USER nonroot:nonroot

EXPOSE 8080

ENTRYPOINT ["/server"]
```

**`docker-compose.yml`**

```yaml
version: "3.9"

services:
  postgres:
    image: postgres:16-alpine
    environment:
      POSTGRES_USER: taskuser
      POSTGRES_PASSWORD: taskpass
      POSTGRES_DB: taskdb
    ports:
      - "5432:5432"
    volumes:
      - postgres_data:/var/lib/postgresql/data
      - ./migrations:/docker-entrypoint-initdb.d  # auto-runs SQL on first start
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U taskuser -d taskdb"]
      interval: 5s
      timeout: 5s
      retries: 5

  api:
    build: .
    ports:
      - "8080:8080"
    environment:
      APP_PORT: 8080
      APP_ENV: production
      DB_HOST: postgres
      DB_PORT: 5432
      DB_USER: taskuser
      DB_PASSWORD: taskpass
      DB_NAME: taskdb
      JWT_SECRET: "replace-with-a-real-secret-in-production"
      JWT_EXPIRY_HOURS: 24
    depends_on:
      postgres:
        condition: service_healthy
    restart: on-failure

volumes:
  postgres_data:
```

Multi-stage build explanation:
- Stage 1 uses the full Go image to compile. The binary is statically linked (`CGO_ENABLED=0`) so it has no libc dependency.
- Stage 2 copies only the binary into a minimal image (~2 MB). This image has no shell, no package manager, no compiler — dramatically reducing the attack surface and image pull time.

---

## 16. Testing

**`internal/handler/task_test.go`**

```go
package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/yourname/task-api/internal/handler"
	"github.com/yourname/task-api/internal/middleware"
	"github.com/yourname/task-api/internal/model"
	"github.com/yourname/task-api/internal/service"
	"github.com/yourname/task-api/pkg/response"
)

func init() {
	// Suppress Gin's debug output in tests.
	gin.SetMode(gin.TestMode)
}

// --- Mock service ---

// MockTaskService implements service.TaskService using testify/mock.
type MockTaskService struct {
	mock.Mock
}

func (m *MockTaskService) Create(ctx context.Context, userID int64, req *model.CreateTaskRequest) (*model.Task, error) {
	args := m.Called(ctx, userID, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*model.Task), args.Error(1)
}

func (m *MockTaskService) GetByID(ctx context.Context, userID, taskID int64) (*model.Task, error) {
	args := m.Called(ctx, userID, taskID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*model.Task), args.Error(1)
}

func (m *MockTaskService) List(ctx context.Context, userID int64, params model.PaginationParams) (*model.ListTasksResponse, error) {
	args := m.Called(ctx, userID, params)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*model.ListTasksResponse), args.Error(1)
}

func (m *MockTaskService) Update(ctx context.Context, userID, taskID int64, req *model.UpdateTaskRequest) (*model.Task, error) {
	args := m.Called(ctx, userID, taskID, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*model.Task), args.Error(1)
}

func (m *MockTaskService) Delete(ctx context.Context, userID, taskID int64) error {
	args := m.Called(ctx, userID, taskID)
	return args.Error(0)
}

// --- Test helpers ---

// setupRouter creates a minimal Gin engine with the task routes.
// It injects a fake userID into the context to simulate auth middleware.
func setupRouter(taskSvc service.TaskService, fakeUserID int64) *gin.Engine {
	r := gin.New()
	h := handler.NewTaskHandler(taskSvc)

	// Middleware that simulates a successful auth check.
	injectUser := func(c *gin.Context) {
		c.Set(middleware.ContextKeyUserID, fakeUserID)
		c.Next()
	}

	tasks := r.Group("/api/v1/tasks")
	tasks.Use(injectUser)
	{
		tasks.GET("", h.List)
		tasks.POST("", h.Create)
		tasks.GET("/:id", h.GetByID)
		tasks.PUT("/:id", h.Update)
		tasks.DELETE("/:id", h.Delete)
	}

	return r
}

// parseResponse reads the response body into a response.Envelope.
func parseResponse(t *testing.T, rec *httptest.ResponseRecorder) response.Envelope {
	t.Helper()
	var env response.Envelope
	err := json.Unmarshal(rec.Body.Bytes(), &env)
	assert.NoError(t, err)
	return env
}

// --- Tests ---

func TestCreate_Success(t *testing.T) {
	mockSvc := new(MockTaskService)
	router := setupRouter(mockSvc, 42)

	req := model.CreateTaskRequest{
		Title:       "Write unit tests",
		Description: "Add tests for all handlers",
		Status:      model.StatusTodo,
	}

	expectedTask := &model.Task{
		ID:          1,
		UserID:      42,
		Title:       req.Title,
		Description: req.Description,
		Status:      model.StatusTodo,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	mockSvc.On("Create", mock.Anything, int64(42), &req).Return(expectedTask, nil)

	body, _ := json.Marshal(req)
	w := httptest.NewRecorder()
	r, _ := http.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewBuffer(body))
	r.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, r)

	assert.Equal(t, http.StatusCreated, w.Code)
	env := parseResponse(t, w)
	assert.True(t, env.Success)
	mockSvc.AssertExpectations(t)
}

func TestCreate_InvalidBody(t *testing.T) {
	mockSvc := new(MockTaskService)
	router := setupRouter(mockSvc, 42)

	// Title is required; sending an empty body should fail binding.
	body := []byte(`{"description": "no title here"}`)
	w := httptest.NewRecorder()
	r, _ := http.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewBuffer(body))
	r.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, r)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	env := parseResponse(t, w)
	assert.False(t, env.Success)
	assert.Contains(t, env.Error, "invalid request body")
	// The service should never be called.
	mockSvc.AssertNotCalled(t, "Create")
}

func TestGetByID_NotFound(t *testing.T) {
	mockSvc := new(MockTaskService)
	router := setupRouter(mockSvc, 42)

	appErr := &service.AppError{Code: 404, Message: "task not found"}
	mockSvc.On("GetByID", mock.Anything, int64(42), int64(99)).Return(nil, appErr)

	w := httptest.NewRecorder()
	r, _ := http.NewRequest(http.MethodGet, "/api/v1/tasks/99", nil)
	router.ServeHTTP(w, r)

	assert.Equal(t, http.StatusNotFound, w.Code)
	env := parseResponse(t, w)
	assert.False(t, env.Success)
	assert.Equal(t, "task not found", env.Error)
	mockSvc.AssertExpectations(t)
}

func TestGetByID_InvalidID(t *testing.T) {
	mockSvc := new(MockTaskService)
	router := setupRouter(mockSvc, 42)

	w := httptest.NewRecorder()
	r, _ := http.NewRequest(http.MethodGet, "/api/v1/tasks/abc", nil)
	router.ServeHTTP(w, r)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	mockSvc.AssertNotCalled(t, "GetByID")
}

func TestList_Success(t *testing.T) {
	mockSvc := new(MockTaskService)
	router := setupRouter(mockSvc, 42)

	params := model.PaginationParams{Page: 1, PerPage: 20}
	mockSvc.On("List", mock.Anything, int64(42), params).Return(&model.ListTasksResponse{
		Tasks:   []*model.Task{},
		Total:   0,
		Page:    1,
		PerPage: 20,
	}, nil)

	w := httptest.NewRecorder()
	r, _ := http.NewRequest(http.MethodGet, "/api/v1/tasks", nil)
	router.ServeHTTP(w, r)

	assert.Equal(t, http.StatusOK, w.Code)
	mockSvc.AssertExpectations(t)
}

func TestDelete_Success(t *testing.T) {
	mockSvc := new(MockTaskService)
	router := setupRouter(mockSvc, 42)

	mockSvc.On("Delete", mock.Anything, int64(42), int64(1)).Return(nil)

	w := httptest.NewRecorder()
	r, _ := http.NewRequest(http.MethodDelete, "/api/v1/tasks/1", nil)
	router.ServeHTTP(w, r)

	assert.Equal(t, http.StatusNoContent, w.Code)
	mockSvc.AssertExpectations(t)
}

func TestUpdate_PartialUpdate(t *testing.T) {
	mockSvc := new(MockTaskService)
	router := setupRouter(mockSvc, 42)

	newTitle := "Updated title"
	req := model.UpdateTaskRequest{Title: &newTitle}
	updatedTask := &model.Task{
		ID: 1, UserID: 42, Title: newTitle, Status: model.StatusTodo,
	}

	mockSvc.On("Update", mock.Anything, int64(42), int64(1), &req).Return(updatedTask, nil)

	body, _ := json.Marshal(req)
	w := httptest.NewRecorder()
	r, _ := http.NewRequest(http.MethodPut, "/api/v1/tasks/1", bytes.NewBuffer(body))
	r.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, r)

	assert.Equal(t, http.StatusOK, w.Code)
	env := parseResponse(t, w)
	assert.True(t, env.Success)
	mockSvc.AssertExpectations(t)
}
```

**`internal/service/task_service_test.go`**

```go
package service_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/yourname/task-api/internal/model"
	"github.com/yourname/task-api/internal/repository"
	"github.com/yourname/task-api/internal/service"
)

// MockTaskRepository implements repository.TaskRepository.
type MockTaskRepository struct {
	mock.Mock
}

func (m *MockTaskRepository) Create(ctx context.Context, task *model.Task) (*model.Task, error) {
	args := m.Called(ctx, task)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*model.Task), args.Error(1)
}

func (m *MockTaskRepository) GetByID(ctx context.Context, id, userID int64) (*model.Task, error) {
	args := m.Called(ctx, id, userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*model.Task), args.Error(1)
}

func (m *MockTaskRepository) List(ctx context.Context, userID int64, params model.PaginationParams) ([]*model.Task, int64, error) {
	args := m.Called(ctx, userID, params)
	if args.Get(0) == nil {
		return nil, args.Get(1).(int64), args.Error(2)
	}
	return args.Get(0).([]*model.Task), args.Get(1).(int64), args.Error(2)
}

func (m *MockTaskRepository) Update(ctx context.Context, task *model.Task) (*model.Task, error) {
	args := m.Called(ctx, task)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*model.Task), args.Error(1)
}

func (m *MockTaskRepository) Delete(ctx context.Context, id, userID int64) error {
	args := m.Called(ctx, id, userID)
	return args.Error(0)
}

func TestTaskService_Create_DefaultStatus(t *testing.T) {
	repo := new(MockTaskRepository)
	svc := service.NewTaskService(repo)
	ctx := context.Background()

	req := &model.CreateTaskRequest{Title: "My task", Description: ""}
	// Status is empty — service should default to "todo".

	expectedTask := &model.Task{ID: 1, Title: "My task", Status: model.StatusTodo}
	repo.On("Create", ctx, mock.MatchedBy(func(t *model.Task) bool {
		return t.Status == model.StatusTodo && t.Title == "My task"
	})).Return(expectedTask, nil)

	task, err := svc.Create(ctx, 1, req)
	assert.NoError(t, err)
	assert.Equal(t, model.StatusTodo, task.Status)
	repo.AssertExpectations(t)
}

func TestTaskService_Create_InvalidStatus(t *testing.T) {
	repo := new(MockTaskRepository)
	svc := service.NewTaskService(repo)
	ctx := context.Background()

	req := &model.CreateTaskRequest{Title: "My task", Status: "flying"}

	_, err := svc.Create(ctx, 1, req)
	assert.Error(t, err)

	var appErr *service.AppError
	assert.ErrorAs(t, err, &appErr)
	assert.Equal(t, 400, appErr.Code)
	repo.AssertNotCalled(t, "Create")
}

func TestTaskService_GetByID_NotFound(t *testing.T) {
	repo := new(MockTaskRepository)
	svc := service.NewTaskService(repo)
	ctx := context.Background()

	repo.On("GetByID", ctx, int64(99), int64(1)).Return(nil, repository.ErrNotFound)

	_, err := svc.GetByID(ctx, 1, 99)
	assert.Error(t, err)

	var appErr *service.AppError
	assert.ErrorAs(t, err, &appErr)
	assert.Equal(t, 404, appErr.Code)
}

func TestTaskService_List_EmptySlice(t *testing.T) {
	repo := new(MockTaskRepository)
	svc := service.NewTaskService(repo)
	ctx := context.Background()

	params := model.PaginationParams{Page: 1, PerPage: 20}
	repo.On("List", ctx, int64(1), params).Return(nil, int64(0), nil)

	result, err := svc.List(ctx, 1, params)
	assert.NoError(t, err)
	// Should never be nil — callers depend on this being a slice.
	assert.NotNil(t, result.Tasks)
	assert.Equal(t, 0, len(result.Tasks))
}

func TestTaskService_Delete_NotFound(t *testing.T) {
	repo := new(MockTaskRepository)
	svc := service.NewTaskService(repo)
	ctx := context.Background()

	repo.On("Delete", ctx, int64(99), int64(1)).Return(repository.ErrNotFound)

	err := svc.Delete(ctx, 1, 99)
	assert.Error(t, err)

	var appErr *service.AppError
	assert.ErrorAs(t, err, &appErr)
	assert.Equal(t, 404, appErr.Code)
}
```

Run the tests:

```bash
go test ./...
```

---

## 17. Running the project

### Option A: Docker Compose (recommended for first run)

```bash
docker compose up --build
```

Docker Compose starts Postgres, applies the migration (the SQL file in `migrations/` is mounted to `/docker-entrypoint-initdb.d/` and run automatically on first boot), then starts the API.

### Option B: Local with a running Postgres

```bash
# Start Postgres however you prefer, then:
psql -h localhost -U taskuser -d taskdb -f migrations/001_create_tasks.sql
go run ./cmd/api
```

### Try the API

**Register:**
```bash
curl -s -X POST http://localhost:8080/api/v1/auth/register \
  -H "Content-Type: application/json" \
  -d '{"email":"test@example.com","password":"password123"}' | jq
```

**Login:**
```bash
TOKEN=$(curl -s -X POST http://localhost:8080/api/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{"email":"test@example.com","password":"password123"}' \
  | jq -r '.data.token')

echo "Token: $TOKEN"
```

**Create a task:**
```bash
curl -s -X POST http://localhost:8080/api/v1/tasks \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"title":"Learn Go","description":"Read the docs","status":"todo"}' | jq
```

**List tasks (with pagination):**
```bash
curl -s "http://localhost:8080/api/v1/tasks?page=1&per_page=10" \
  -H "Authorization: Bearer $TOKEN" | jq
```

**Update a task:**
```bash
curl -s -X PUT http://localhost:8080/api/v1/tasks/1 \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"status":"in_progress"}' | jq
```

**Delete a task:**
```bash
curl -s -X DELETE http://localhost:8080/api/v1/tasks/1 \
  -H "Authorization: Bearer $TOKEN"
# Returns 204 No Content
```

**Health check:**
```bash
curl -s http://localhost:8080/health | jq
```

---

## 18. What to explore next

**Immediate improvements for this project:**

1. **Proper migrations** — add `golang-migrate/migrate` so you can run `migrate up` / `migrate down` and track schema version in the database.

2. **Input sanitization** — the binding tags handle structural validation, but you might want to trim whitespace from `Title` in the service layer before persisting.

3. **Refresh tokens** — the current design issues one JWT per login. A production system issues a short-lived access token and a long-lived refresh token stored in the database, allowing revocation.

4. **Redis rate limiter** — the current in-memory rate limiter resets when the process restarts and does not work across multiple replicas. Replace it with a sliding window counter in Redis.

5. **OpenAPI / Swagger** — `swaggo/swag` can generate an OpenAPI spec from code comments. Useful for frontend teams.

6. **Metrics** — add a `/metrics` endpoint with Prometheus counters for request count, error count, and latency histograms. Then create a Grafana dashboard.

7. **Database-level soft deletes** — add a `deleted_at TIMESTAMPTZ` column and filter it in all queries so deleted tasks are recoverable.

**Deeper Go topics to read next:**

- `context.Context` propagation — how timeouts flow from HTTP handler through service to database
- `sync.Pool` — object reuse for high-throughput paths
- `pprof` — profiling CPU and memory in production
- `embed.FS` — bundling the migrations directory into the binary so there is no external dependency at runtime
- `errgroup` — running multiple goroutines with coordinated cancellation (useful for fan-out data fetching in handlers)

---

*End of Chapter 4. You now have a complete, production-grade REST API in Go — structured, tested, containerized, and ready to deploy.*
