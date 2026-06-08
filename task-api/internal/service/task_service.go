package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/loilh/task-api/internal/model"
	"github.com/loilh/task-api/internal/repository"
)

type AppError struct {
	Code    int
	Message string
	Err     error
}

func (e *AppError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("service: %s: %v", e.Message, e.Err)
	}
	return e.Message
}

func (e *AppError) Unwrap() error {
	return e.Err
}

func newAppError(code int, message string, cause error) *AppError {
	return &AppError{
		Code:    code,
		Message: message,
		Err:     cause,
	}
}

type TaskService interface {
	Create(ctx context.Context, userID int64, req *model.CreateTaskRequest) error
	Update(ctx context.Context, userID int64, taskID int64, req *model.UpdateTaskRequest) error
	Delete(ctx context.Context, userID int64, taskID int64) error
	GetByID(ctx context.Context, userID int64, taskID int64) (*model.Task, error)
	List(ctx context.Context, userID int64, params *model.PaginationParams) ([]*model.Task, int64, error)
}

type taskService struct {
	repo repository.TaskRepository
}

func NewTaskService(repo repository.TaskRepository) TaskService {
	return &taskService{repo: repo}
}

func (s *taskService) Create(ctx context.Context, userID int64, req *model.CreateTaskRequest) error {
	status := req.Status
	if status == "" {
		status = model.StatusTodo
	}
	if !status.IsValid() {
		return newAppError(400, fmt.Sprintf("Invalid status %q; must be %s, %s, or %s", status, model.StatusTodo, model.StatusInProgress, model.StatusDone), nil)
	}
	task := &model.Task{
		Title:       req.Title,
		Description: req.Description,
		Status:      status,
		UserID:      userID,
	}

	err := s.repo.Create(ctx, task)
	if err != nil {
		return newAppError(500, "Failed to create task", err)
	}
	return nil
}

func (s *taskService) Update(ctx context.Context, userID int64, taskID int64, req *model.UpdateTaskRequest) error {
	existing, err := s.repo.GetByID(ctx, taskID, userID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return newAppError(404, "Task not found", nil)
		}
		return newAppError(500, "Failed to get task", err)
	}

	if req.Title != nil {
		existing.Title = *req.Title
	}
	if req.Description != nil {
		existing.Description = *req.Description
	}
	if req.Status != nil {
		newStatus := *req.Status
		if newStatus != existing.Status {
			if !newStatus.IsValid() {
				return newAppError(400, fmt.Sprintf("Invalid status %q; must be %s, %s, or %s", newStatus, model.StatusTodo, model.StatusInProgress, model.StatusDone), nil)
			}
			existing.Status = newStatus
		}
	}

	err = s.repo.Update(ctx, existing)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return newAppError(404, "Task not found", nil)
		}
		return newAppError(500, "Failed to update task", err)
	}
	return nil
}

func (s *taskService) Delete(ctx context.Context, userID int64, taskID int64) error {
	err := s.repo.Delete(ctx, taskID, userID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return newAppError(404, "Task not found", nil)
		}
		return newAppError(500, "Failed to delete task", err)
	}
	return nil
}

func (s *taskService) GetByID(ctx context.Context, userID int64, taskID int64) (*model.Task, error) {
	task, err := s.repo.GetByID(ctx, taskID, userID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, newAppError(404, "Task not found", nil)
		}
		return nil, newAppError(500, "Failed to get task", err)
	}
	return task, nil
}

func (s *taskService) List(ctx context.Context, userID int64, params *model.PaginationParams) ([]*model.Task, int64, error) {
	if params == nil {
		params = &model.PaginationParams{}
	}
	params.Normalize()
	tasks, total, err := s.repo.List(ctx, userID, params)
	if err != nil {
		return nil, 0, newAppError(500, "Failed to list tasks", err)
	}

	return tasks, total, nil
}
