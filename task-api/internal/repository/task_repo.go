package repository

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/loilh/task-api/internal/model"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("record not found")

type TaskRepository interface {
	Create(ctx context.Context, task *model.Task) error
	Update(ctx context.Context, task *model.Task) error
	GetByID(ctx context.Context, id, userID int64) (*model.Task, error)
	List(ctx context.Context, userID int64, params *model.PaginationParams) ([]*model.Task, int64, error)
	Delete(ctx context.Context, id, userID int64) error
}

type UserRepository interface {
	Create(ctx context.Context, user *model.User) error
	GetByEmail(ctx context.Context, email string) (*model.User, error)
	GetByID(ctx context.Context, id int64) (*model.User, error)
}

type pgTaskRepository struct {
	pool *pgxpool.Pool
}

func NewTaskRepository(pool *pgxpool.Pool) TaskRepository {
	return &pgTaskRepository{pool: pool}
}

func (r *pgTaskRepository) Create(ctx context.Context, task *model.Task) error {
	query := `
		INSERT INTO tasks (user_id, title, description, status)
		VALUES ($1, $2, $3, $4)
		RETURNING id, user_id, title, description, status, created_at, updated_at
	`

	row := r.pool.QueryRow(ctx, query, task.UserID, task.Title, task.Description, task.Status)

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
		return fmt.Errorf("repository: create task: %w", err)
	}
	return nil
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

func (r *pgTaskRepository) List(ctx context.Context, userID int64, params *model.PaginationParams) ([]*model.Task, int64, error) {
	// Run count and data queries in parallel using a transaction for consistency.
	var wg sync.WaitGroup

	var total int64
	tasks := make([]*model.Task, 0)
	var countErr, queryErr error

	wg.Add(2)

	go func() {
		defer wg.Done()
		countErr = r.pool.QueryRow(ctx,
			"SELECT COUNT(*) FROM tasks WHERE user_id = $1", userID,
		).Scan(&total)
	}()

	go func() {
		defer wg.Done()

		query := `
		SELECT id, user_id, title, description, status, created_at, updated_at
		FROM tasks
		WHERE user_id = $1
		ORDER BY created_at DESC
		LIMIT $2 OFFSET $3`

		rows, err := r.pool.Query(ctx, query, userID, params.PerPage, params.Offset())
		if err != nil {
			queryErr = err
			return
		}
		defer rows.Close()

		for rows.Next() {
			var t model.Task
			if err := rows.Scan(
				&t.ID, &t.UserID, &t.Title, &t.Description,
				&t.Status, &t.CreatedAt, &t.UpdatedAt,
			); err != nil {
				queryErr = fmt.Errorf("repository: list tasks scan: %w", err)
				return
			}
			tasks = append(tasks, &t)
		}

		if err := rows.Err(); err != nil {
			queryErr = fmt.Errorf("repository: list tasks rows: %w", err)
		}

	}()

	wg.Wait()

	if countErr != nil {
		return nil, 0, fmt.Errorf("repository: list tasks count: %w", countErr)
	}

	if queryErr != nil {
		return nil, 0, fmt.Errorf("repository: list tasks query: %w", queryErr)
	}

	return tasks, total, nil
}

func (r *pgTaskRepository) Update(ctx context.Context, task *model.Task) error {
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
			return ErrNotFound
		}
		return fmt.Errorf("repository: update task: %w", err)
	}
	return nil
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

type pgUserRepository struct {
	pool *pgxpool.Pool
}

// NewUserRepository returns a PostgreSQL-backed UserRepository.
func NewUserRepository(pool *pgxpool.Pool) UserRepository {
	return &pgUserRepository{pool: pool}
}

func (r *pgUserRepository) Create(ctx context.Context, user *model.User) error {
	query := `
		INSERT INTO users (email, password_hash)
		VALUES ($1, $2)
		RETURNING id, email, password_hash, created_at
	`
	row := r.pool.QueryRow(ctx, query, user.Email, user.PasswordHash)

	var created model.User
	err := row.Scan(&created.ID, &created.Email, &created.PasswordHash, &created.CreatedAt)
	if err != nil {
		return fmt.Errorf("repository: create user: %w", err)
	}
	return nil
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
