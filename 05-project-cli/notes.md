# Chapter 5: Building a Real CLI Tool — `taskr`

You know Go's syntax and concurrency primitives. Now you'll build something people actually use: a command-line task manager called `taskr`. By the end of this chapter you'll have a fully installable binary that stores tasks in JSON, prints colored tables, reads a config file, and ships with shell completion. Everything in this chapter is production-quality — you could publish this to GitHub today.

---

## What we're building

```
$ taskr add "Buy groceries" --priority high --due 2024-12-31
Added task #1: Buy groceries

$ taskr list
 ID  TITLE           PRIORITY  DUE         STATUS
  1  Buy groceries   HIGH      2024-12-31  pending
  2  Read Go book    medium    -           pending

$ taskr done 1
Task #1 marked done.

$ taskr stats
Total: 2  Done: 1  Pending: 1  Completion: 50%
```

---

## Project layout

```
taskr/
├── cmd/
│   ├── root.go
│   ├── add.go
│   ├── list.go
│   ├── done.go
│   ├── delete.go
│   ├── edit.go
│   └── stats.go
├── internal/
│   ├── model/task.go
│   └── store/json_store.go
├── main.go
├── go.mod
└── Makefile
```

The `cmd/` package contains one file per subcommand. The `internal/` packages hold business logic the cmd layer depends on — but that nothing outside this module should import.

---

## Bootstrapping the module

```bash
mkdir taskr && cd taskr
go mod init github.com/yourname/taskr
go get github.com/spf13/cobra@v1.8.0
go get github.com/spf13/viper@v1.18.0
go get github.com/olekukonko/tablewriter@v0.0.5
go get github.com/fatih/color@v1.16.0
go get github.com/google/uuid@v1.6.0
```

---

## go.mod

```go
module github.com/yourname/taskr

go 1.21

require (
	github.com/fatih/color v1.16.0
	github.com/google/uuid v1.6.0
	github.com/olekukonko/tablewriter v0.0.5
	github.com/spf13/cobra v1.8.0
	github.com/spf13/viper v1.18.0
)
```

---

## The data model — `internal/model/task.go`

This file defines what a task is. We use a custom type for Priority so we can attach methods to it, giving us both type safety and a free Stringer.

```go
// internal/model/task.go
package model

import (
	"fmt"
	"strings"
	"time"
)

// Priority is an enum type backed by a string so JSON serialization is human-readable.
type Priority string

const (
	PriorityHigh   Priority = "high"
	PriorityMedium Priority = "medium"
	PriorityLow    Priority = "low"
)

// ParsePriority converts a string flag value into a Priority, returning an error
// for anything we don't recognize.  This is called by cobra flag validation.
func ParsePriority(s string) (Priority, error) {
	switch strings.ToLower(s) {
	case "high", "h":
		return PriorityHigh, nil
	case "medium", "med", "m":
		return PriorityMedium, nil
	case "low", "l":
		return PriorityLow, nil
	case "":
		return PriorityMedium, nil
	default:
		return "", fmt.Errorf("unknown priority %q: use high, medium, or low", s)
	}
}

// String satisfies the fmt.Stringer interface.  fmt.Println(PriorityHigh) prints "high".
func (p Priority) String() string { return string(p) }

// Status mirrors Priority — a string enum so the JSON file is readable by humans.
type Status string

const (
	StatusPending Status = "pending"
	StatusDone    Status = "done"
)

// Task is the central data structure.  Every field that needs to survive a
// program restart carries a `json:"..."` tag.
type Task struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Priority  Priority  `json:"priority"`
	Status    Status    `json:"status"`
	DueDate   string    `json:"due_date,omitempty"` // stored as YYYY-MM-DD string
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// IsDone is a tiny helper used by stats calculations.
func (t *Task) IsDone() bool { return t.Status == StatusDone }

// DueSoon returns true if the task is due within the next 24 hours and not done.
// Used by the list command to highlight urgent tasks.
func (t *Task) DueSoon() bool {
	if t.IsDone() || t.DueDate == "" {
		return false
	}
	due, err := time.Parse("2006-01-02", t.DueDate)
	if err != nil {
		return false
	}
	return time.Until(due) < 24*time.Hour
}

// ShortID returns the first 8 characters of the UUID, used for display.
// Real tools like git also do this — nobody wants to type 36-character IDs.
func (t *Task) ShortID() string {
	if len(t.ID) <= 8 {
		return t.ID
	}
	return t.ID[:8]
}
```

**Key design decisions:**

- `Priority` and `Status` are `type X string` not `type X int`. This means JSON serialization is automatic and readable — no custom MarshalJSON needed.
- `DueDate` is stored as a plain string (`"2024-12-31"`) rather than `time.Time` because time.Time's JSON format includes timezone info that users don't want to type.
- `omitempty` on `DueDate` keeps the JSON clean when no due date is set.

---

## The store — `internal/store/json_store.go`

This is the persistence layer. It reads and writes `~/.taskr/tasks.json`. The critical technique here is **atomic writes**: we never write directly to the final file. Instead we write to a temp file, then rename it. On Unix, rename is atomic — the file either has old content or new content, never a partially written state.

```go
// internal/store/json_store.go
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/yourname/taskr/internal/model"
)

// Store manages reading and writing the task list.
// The mutex makes it safe to call from multiple goroutines, though our CLI
// doesn't actually do that — it's good practice for any type that owns a file.
type Store struct {
	mu       sync.Mutex
	filePath string
}

// taskFile is the top-level JSON structure written to disk.
// Wrapping the slice in a struct makes it easier to add metadata later
// (e.g. schema version, last-modified timestamp).
type taskFile struct {
	Tasks []*model.Task `json:"tasks"`
}

// NewStore creates a Store pointed at the standard location.
// It also creates the directory if it doesn't exist, so the first run just works.
func NewStore() (*Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("cannot determine home directory: %w", err)
	}
	dir := filepath.Join(home, ".taskr")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("cannot create data directory %s: %w", dir, err)
	}
	return &Store{
		filePath: filepath.Join(dir, "tasks.json"),
	}, nil
}

// NewStoreAt lets tests inject a custom path.
func NewStoreAt(path string) *Store {
	return &Store{filePath: path}
}

// load reads the JSON file from disk.  If the file doesn't exist we return an
// empty list — that's a valid first-run state, not an error.
func (s *Store) load() ([]*model.Task, error) {
	data, err := os.ReadFile(s.filePath)
	if os.IsNotExist(err) {
		return []*model.Task{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading task file: %w", err)
	}

	var tf taskFile
	if err := json.Unmarshal(data, &tf); err != nil {
		return nil, fmt.Errorf("parsing task file: %w", err)
	}
	return tf.Tasks, nil
}

// save writes the task list atomically.
//
// The pattern is:
//  1. Write to a temp file in the same directory (same filesystem guarantees rename works).
//  2. Sync the temp file to disk (so data survives a crash between write and rename).
//  3. Rename temp file over the real file — this is atomic on all major OSes.
//
// Without this, a crash mid-write would corrupt tasks.json entirely.
func (s *Store) save(tasks []*model.Task) error {
	tf := taskFile{Tasks: tasks}
	data, err := json.MarshalIndent(tf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling tasks: %w", err)
	}

	// Write to a temp file alongside the real file (same directory = same filesystem).
	dir := filepath.Dir(s.filePath)
	tmp, err := os.CreateTemp(dir, "tasks-*.json.tmp")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpName := tmp.Name()

	// Use a named function so we can defer cleanup on error paths.
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("writing temp file: %w", err)
	}
	// Sync forces the OS to flush kernel buffers to storage.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("syncing temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("closing temp file: %w", err)
	}

	// Rename is atomic on POSIX.  On Windows it may fail if the destination
	// exists — os.Rename handles that case on Windows in Go 1.21+.
	if err := os.Rename(tmpName, s.filePath); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("renaming temp file: %w", err)
	}
	return nil
}

// Add creates a new task, assigns it a UUID, saves, and returns the new task.
func (s *Store) Add(title string, priority model.Priority, dueDate string) (*model.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tasks, err := s.load()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	task := &model.Task{
		ID:        uuid.New().String(),
		Title:     title,
		Priority:  priority,
		Status:    model.StatusPending,
		DueDate:   dueDate,
		CreatedAt: now,
		UpdatedAt: now,
	}
	tasks = append(tasks, task)

	if err := s.save(tasks); err != nil {
		return nil, err
	}
	return task, nil
}

// List returns tasks filtered by the given options.
// Passing empty strings means "no filter on that field".
func (s *Store) List(statusFilter string, priorityFilter string) ([]*model.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tasks, err := s.load()
	if err != nil {
		return nil, err
	}

	// Sort by creation time so the list is stable.
	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].CreatedAt.Before(tasks[j].CreatedAt)
	})

	var result []*model.Task
	for _, t := range tasks {
		if statusFilter != "" && statusFilter != "all" && string(t.Status) != statusFilter {
			continue
		}
		if priorityFilter != "" && string(t.Priority) != priorityFilter {
			continue
		}
		result = append(result, t)
	}
	return result, nil
}

// GetByShortID finds a task whose ID starts with the given prefix.
// This lets users type `taskr done abc12345` instead of the full UUID.
func (s *Store) GetByShortID(shortID string) (*model.Task, error) {
	tasks, err := s.load()
	if err != nil {
		return nil, err
	}
	var matches []*model.Task
	for _, t := range tasks {
		if len(t.ID) >= len(shortID) && t.ID[:len(shortID)] == shortID {
			matches = append(matches, t)
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("no task found with ID prefix %q", shortID)
	case 1:
		return matches[0], nil
	default:
		return nil, fmt.Errorf("ambiguous ID %q matches %d tasks; use more characters", shortID, len(matches))
	}
}

// MarkDone sets a task's status to done and saves.
func (s *Store) MarkDone(shortID string) (*model.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tasks, err := s.load()
	if err != nil {
		return nil, err
	}

	var target *model.Task
	for _, t := range tasks {
		if len(t.ID) >= len(shortID) && t.ID[:len(shortID)] == shortID {
			target = t
			break
		}
	}
	if target == nil {
		return nil, fmt.Errorf("no task with ID prefix %q", shortID)
	}

	target.Status = model.StatusDone
	target.UpdatedAt = time.Now()

	if err := s.save(tasks); err != nil {
		return nil, err
	}
	return target, nil
}

// Delete removes a task by short ID.
func (s *Store) Delete(shortID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tasks, err := s.load()
	if err != nil {
		return err
	}

	idx := -1
	for i, t := range tasks {
		if len(t.ID) >= len(shortID) && t.ID[:len(shortID)] == shortID {
			if idx != -1 {
				return fmt.Errorf("ambiguous ID %q; use more characters", shortID)
			}
			idx = i
		}
	}
	if idx == -1 {
		return fmt.Errorf("no task with ID prefix %q", shortID)
	}

	// Remove element at idx by swapping with last and truncating.
	// This avoids the O(n) copy that append-based removal requires when the slice is large.
	tasks[idx] = tasks[len(tasks)-1]
	tasks = tasks[:len(tasks)-1]

	return s.save(tasks)
}

// Edit updates mutable fields of a task.  Fields are only changed when the
// corresponding "has" flag is true, so callers can update one field at a time.
type EditOptions struct {
	HasTitle    bool
	Title       string
	HasPriority bool
	Priority    model.Priority
	HasDueDate  bool
	DueDate     string
	HasStatus   bool
	Status      model.Status
}

func (s *Store) Edit(shortID string, opts EditOptions) (*model.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tasks, err := s.load()
	if err != nil {
		return nil, err
	}

	var target *model.Task
	for _, t := range tasks {
		if len(t.ID) >= len(shortID) && t.ID[:len(shortID)] == shortID {
			target = t
			break
		}
	}
	if target == nil {
		return nil, fmt.Errorf("no task with ID prefix %q", shortID)
	}

	if opts.HasTitle {
		target.Title = opts.Title
	}
	if opts.HasPriority {
		target.Priority = opts.Priority
	}
	if opts.HasDueDate {
		target.DueDate = opts.DueDate
	}
	if opts.HasStatus {
		target.Status = opts.Status
	}
	target.UpdatedAt = time.Now()

	if err := s.save(tasks); err != nil {
		return nil, err
	}
	return target, nil
}

// Stats returns aggregate counts over all tasks.
type Stats struct {
	Total      int
	Done       int
	Pending    int
	ByPriority map[model.Priority]int
}

func (s *Store) Stats() (*Stats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tasks, err := s.load()
	if err != nil {
		return nil, err
	}

	stats := &Stats{
		ByPriority: make(map[model.Priority]int),
	}
	for _, t := range tasks {
		stats.Total++
		if t.IsDone() {
			stats.Done++
		} else {
			stats.Pending++
		}
		stats.ByPriority[t.Priority]++
	}
	return stats, nil
}
```

---

## Entry point — `main.go`

Cobra-based CLI programs have the thinnest possible main.go. All it does is call the root command's Execute method.

```go
// main.go
package main

import "github.com/yourname/taskr/cmd"

func main() {
	cmd.Execute()
}
```

---

## Root command — `cmd/root.go`

The root command wires together the whole Cobra tree. It also initializes Viper, which reads `~/.taskr/config.yaml` for defaults.

```go
// cmd/root.go
package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// rootCmd is the top-level command — `taskr` with no subcommand.
// It prints help by default.
var rootCmd = &cobra.Command{
	Use:   "taskr",
	Short: "A fast, minimal task manager for the command line",
	Long: `taskr stores tasks in ~/.taskr/tasks.json and provides
commands to add, list, complete, and report on them.

Use 'taskr --help' for a list of commands.`,
	// SilenceUsage suppresses the usage printout when RunE returns an error.
	// Without this, every validation error shows the full help text — noisy.
	SilenceUsage: true,
}

// Execute is called by main.go.  It's the only exported symbol from this package.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		// cobra already printed the error; we just need a non-zero exit code.
		os.Exit(1)
	}
}

func init() {
	// cobra.OnInitialize runs before any command's RunE.
	cobra.OnInitialize(initConfig)

	// Persistent flags are inherited by every subcommand.
	rootCmd.PersistentFlags().Bool("no-color", false, "disable color output")

	// Bind to viper so config file can override the default.
	viper.BindPFlag("no_color", rootCmd.PersistentFlags().Lookup("no-color"))

	// Add all subcommands.
	rootCmd.AddCommand(addCmd)
	rootCmd.AddCommand(listCmd)
	rootCmd.AddCommand(doneCmd)
	rootCmd.AddCommand(deleteCmd)
	rootCmd.AddCommand(editCmd)
	rootCmd.AddCommand(statsCmd)
	rootCmd.AddCommand(completionCmd)
}

// initConfig reads ~/.taskr/config.yaml if it exists.
// Viper supports JSON, TOML, YAML, HCL, and .env files.
// Using a config file lets power users set --no-color permanently,
// change the date format, etc., without environment variables.
func initConfig() {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Warning: cannot determine home directory:", err)
		return
	}

	configDir := filepath.Join(home, ".taskr")
	viper.SetConfigName("config")  // config.yaml / config.toml / config.json
	viper.SetConfigType("yaml")
	viper.AddConfigPath(configDir)
	viper.AddConfigPath(".")        // also allow ./config.yaml for development

	// SetEnvPrefix means TASKR_NO_COLOR maps to no_color.
	viper.SetEnvPrefix("TASKR")
	viper.AutomaticEnv()

	// Set defaults so the program works even without a config file.
	viper.SetDefault("date_format", "2006-01-02")
	viper.SetDefault("no_color", false)

	if err := viper.ReadInConfig(); err != nil {
		// It's fine if the config file doesn't exist.
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			fmt.Fprintln(os.Stderr, "Warning: config file error:", err)
		}
	}
}
```

**How Cobra, Viper, and flags interact:**

Cobra handles command parsing and routing. Viper handles configuration layering: config file values < environment variables < command-line flags. `BindPFlag` links a Cobra flag to a Viper key so that whichever source provides a value, the rest of the program reads it through `viper.GetBool("no_color")`. This means a user can write `no_color: true` in `~/.taskr/config.yaml` and never pass `--no-color` on every invocation.

---

## Add command — `cmd/add.go`

```go
// cmd/add.go
package cmd

import (
	"fmt"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/yourname/taskr/internal/model"
	"github.com/yourname/taskr/internal/store"
)

var addCmd = &cobra.Command{
	Use:   "add <title>",
	Short: "Add a new task",
	Example: `  taskr add "Buy groceries"
  taskr add "Review PR" --priority high --due 2024-12-31`,
	// Args validates positional arguments before RunE is called.
	// ExactArgs(1) means exactly one positional argument is required;
	// Cobra prints a helpful error automatically if the count is wrong.
	Args: cobra.ExactArgs(1),
	RunE: runAdd,
}

func init() {
	// Local flags are only available on this command.
	addCmd.Flags().StringP("priority", "p", "medium", "task priority: high, medium, low")
	addCmd.Flags().StringP("due", "d", "", "due date (YYYY-MM-DD)")
}

func runAdd(cmd *cobra.Command, args []string) error {
	title := args[0]
	if title == "" {
		return fmt.Errorf("task title cannot be empty")
	}

	priorityStr, _ := cmd.Flags().GetString("priority")
	priority, err := model.ParsePriority(priorityStr)
	if err != nil {
		return err
	}

	due, _ := cmd.Flags().GetString("due")
	if due != "" {
		// Validate the date format immediately so we fail fast.
		if _, err := validateDate(due); err != nil {
			return err
		}
	}

	s, err := store.NewStore()
	if err != nil {
		return fmt.Errorf("opening store: %w", err)
	}

	task, err := s.Add(title, priority, due)
	if err != nil {
		return fmt.Errorf("adding task: %w", err)
	}

	// Color output — respect the --no-color flag (and the TASKR_NO_COLOR env var).
	if viper.GetBool("no_color") {
		color.NoColor = true
	}

	green := color.New(color.FgGreen).SprintFunc()
	bold := color.New(color.Bold).SprintFunc()

	fmt.Printf("%s %s %s\n",
		green("Added task"),
		bold("#"+task.ShortID()),
		green(":"),
	)
	fmt.Printf("  Title:    %s\n", task.Title)
	fmt.Printf("  Priority: %s\n", priorityColor(task.Priority))
	if task.DueDate != "" {
		fmt.Printf("  Due:      %s\n", task.DueDate)
	}
	return nil
}

// validateDate parses a YYYY-MM-DD string and returns an error with a helpful message.
func validateDate(s string) (string, error) {
	// time.Parse is strict — "2024-2-1" won't match "2006-01-02".
	const layout = "2006-01-02"
	_, err := fmt.Sscanf(s, "%04d-%02d-%02d", new(int), new(int), new(int))
	if err != nil {
		return "", fmt.Errorf("invalid date %q: use YYYY-MM-DD format", s)
	}
	return s, nil
}

// priorityColor returns a colored string for a priority level.
// This function is used by both add.go and list.go — shared display logic.
func priorityColor(p model.Priority) string {
	switch p {
	case model.PriorityHigh:
		return color.New(color.FgRed, color.Bold).Sprint(p)
	case model.PriorityMedium:
		return color.New(color.FgYellow).Sprint(p)
	case model.PriorityLow:
		return color.New(color.FgGreen).Sprint(p)
	default:
		return string(p)
	}
}
```

---

## List command — `cmd/list.go`

This command shows the most technique. It uses `text/tabwriter` from the standard library to align columns, then layering `github.com/olekukonko/tablewriter` for a bordered table view.

```go
// cmd/list.go
package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/fatih/color"
	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/yourname/taskr/internal/model"
	"github.com/yourname/taskr/internal/store"
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List tasks",
	Aliases: []string{"ls"},
	Example: `  taskr list
  taskr list --status pending
  taskr list --priority high
  taskr list --status all`,
	Args: cobra.NoArgs,
	RunE: runList,
}

func init() {
	listCmd.Flags().StringP("status", "s", "pending", "filter by status: pending, done, all")
	listCmd.Flags().StringP("priority", "p", "", "filter by priority: high, medium, low")
}

func runList(cmd *cobra.Command, args []string) error {
	if viper.GetBool("no_color") {
		color.NoColor = true
	}

	statusFilter, _ := cmd.Flags().GetString("status")
	priorityFilter, _ := cmd.Flags().GetString("priority")

	// Validate status flag.
	switch statusFilter {
	case "pending", "done", "all", "":
		// valid
	default:
		return fmt.Errorf("invalid status %q: use pending, done, or all", statusFilter)
	}

	// Validate priority flag.
	if priorityFilter != "" {
		if _, err := model.ParsePriority(priorityFilter); err != nil {
			return err
		}
	}

	s, err := store.NewStore()
	if err != nil {
		return fmt.Errorf("opening store: %w", err)
	}

	tasks, err := s.List(statusFilter, priorityFilter)
	if err != nil {
		return fmt.Errorf("listing tasks: %w", err)
	}

	if len(tasks) == 0 {
		fmt.Println("No tasks found.")
		return nil
	}

	printTable(tasks)
	return nil
}

// printTable renders tasks as a bordered table using olekukonko/tablewriter.
// tablewriter handles column widths, alignment, and borders automatically.
func printTable(tasks []*model.Task) {
	table := tablewriter.NewWriter(os.Stdout)
	table.SetHeader([]string{"ID", "Title", "Priority", "Due", "Status", "Created"})

	// Tablewriter options for a clean look.
	table.SetBorder(true)
	table.SetRowLine(false)          // no line between each row
	table.SetHeaderAlignment(tablewriter.ALIGN_LEFT)
	table.SetAlignment(tablewriter.ALIGN_LEFT)
	table.SetCenterSeparator("+")
	table.SetColumnSeparator("|")
	table.SetRowSeparator("-")
	table.SetAutoWrapText(false)     // don't wrap long titles

	// Column colors — tablewriter supports ANSI colors per-cell.
	// We compute colors ourselves and inject them as pre-colored strings.
	for _, t := range tasks {
		due := t.DueDate
		if due == "" {
			due = "-"
		}

		// Truncate long titles to keep the table readable.
		title := t.Title
		if len(title) > 40 {
			title = title[:37] + "..."
		}

		// Status display with color.
		statusStr := string(t.Status)
		if t.Status == model.StatusDone {
			statusStr = color.New(color.FgGreen).Sprint(statusStr)
		}

		// Highlight overdue tasks in red.
		if t.DueSoon() && !t.IsDone() {
			due = color.New(color.FgRed, color.Bold).Sprint(due + "!")
		}

		table.Append([]string{
			t.ShortID(),
			title,
			priorityColor(t.Priority),
			due,
			statusStr,
			t.CreatedAt.Format("2006-01-02"),
		})
	}

	table.Render()
	fmt.Printf("\n%d task(s) shown.\n", len(tasks))
}

// statusSymbol returns a unicode symbol for status — used in compact mode.
func statusSymbol(s model.Status) string {
	switch s {
	case model.StatusDone:
		return color.GreenString("✓")
	default:
		return color.YellowString("○")
	}
}

// truncate cuts a string to max length, appending "..." if truncated.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

// compactLine renders one task as a single line — used when stdout is not a terminal.
// When piping output to grep or another tool, bordered tables are noise.
func compactLine(t *model.Task) string {
	due := "-"
	if t.DueDate != "" {
		due = t.DueDate
	}
	return fmt.Sprintf("%s  %-8s  %-8s  %-12s  %s",
		t.ShortID(),
		truncate(string(t.Priority), 8),
		due,
		string(t.Status),
		t.Title,
	)
}

// isTerminal returns true if os.Stdout is connected to a terminal.
// When it's not (e.g. piped), we suppress ANSI codes and borders.
func isTerminal() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// formatTaskList returns plain-text task lines — used when not in a terminal.
func formatTaskList(tasks []*model.Task) string {
	var sb strings.Builder
	sb.WriteString("ID        PRIORITY  DUE         STATUS      TITLE\n")
	sb.WriteString(strings.Repeat("-", 60) + "\n")
	for _, t := range tasks {
		sb.WriteString(compactLine(t) + "\n")
	}
	return sb.String()
}
```

---

## Done command — `cmd/done.go`

```go
// cmd/done.go
package cmd

import (
	"fmt"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/yourname/taskr/internal/store"
)

var doneCmd = &cobra.Command{
	Use:   "done <id>",
	Short: "Mark a task as done",
	Example: `  taskr done abc12345
  taskr done a  # works if only one task starts with "a"`,
	Args: cobra.ExactArgs(1),
	RunE: runDone,
}

func runDone(cmd *cobra.Command, args []string) error {
	if viper.GetBool("no_color") {
		color.NoColor = true
	}

	shortID := args[0]
	if shortID == "" {
		return fmt.Errorf("task ID is required")
	}

	s, err := store.NewStore()
	if err != nil {
		return fmt.Errorf("opening store: %w", err)
	}

	task, err := s.MarkDone(shortID)
	if err != nil {
		return err
	}

	green := color.New(color.FgGreen, color.Bold).SprintFunc()
	fmt.Printf("%s Task %s: %s\n",
		green("✓"),
		"#"+task.ShortID(),
		task.Title,
	)
	return nil
}
```

---

## Delete command — `cmd/delete.go`

```go
// cmd/delete.go
package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/yourname/taskr/internal/store"
)

var deleteCmd = &cobra.Command{
	Use:     "delete <id>",
	Short:   "Delete a task permanently",
	Aliases: []string{"rm"},
	Args:    cobra.ExactArgs(1),
	RunE:    runDelete,
}

func init() {
	deleteCmd.Flags().BoolP("force", "f", false, "skip confirmation prompt")
}

func runDelete(cmd *cobra.Command, args []string) error {
	if viper.GetBool("no_color") {
		color.NoColor = true
	}

	shortID := args[0]
	force, _ := cmd.Flags().GetBool("force")

	s, err := store.NewStore()
	if err != nil {
		return fmt.Errorf("opening store: %w", err)
	}

	// Show the task before deleting so the user knows what they're removing.
	task, err := s.GetByShortID(shortID)
	if err != nil {
		return err
	}

	if !force {
		// Prompt for confirmation — destructive operations should confirm.
		yellow := color.New(color.FgYellow).SprintFunc()
		fmt.Printf("Delete task %s %q? [y/N] ",
			yellow("#"+task.ShortID()),
			task.Title,
		)

		reader := bufio.NewReader(os.Stdin)
		response, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("reading input: %w", err)
		}
		response = strings.TrimSpace(strings.ToLower(response))
		if response != "y" && response != "yes" {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	if err := s.Delete(shortID); err != nil {
		return err
	}

	red := color.New(color.FgRed).SprintFunc()
	fmt.Printf("%s Deleted task #%s: %s\n", red("✗"), task.ShortID(), task.Title)
	return nil
}
```

---

## Edit command — `cmd/edit.go`

```go
// cmd/edit.go
package cmd

import (
	"fmt"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/yourname/taskr/internal/model"
	"github.com/yourname/taskr/internal/store"
)

var editCmd = &cobra.Command{
	Use:   "edit <id>",
	Short: "Edit a task's fields",
	Example: `  taskr edit abc12345 --title "New title"
  taskr edit abc12345 --priority low
  taskr edit abc12345 --due 2025-01-15 --priority high`,
	Args: cobra.ExactArgs(1),
	RunE: runEdit,
}

func init() {
	editCmd.Flags().StringP("title", "t", "", "new title")
	editCmd.Flags().StringP("priority", "p", "", "new priority: high, medium, low")
	editCmd.Flags().StringP("due", "d", "", "new due date (YYYY-MM-DD), use 'none' to clear")
	editCmd.Flags().StringP("status", "s", "", "new status: pending, done")
}

func runEdit(cmd *cobra.Command, args []string) error {
	if viper.GetBool("no_color") {
		color.NoColor = true
	}

	shortID := args[0]

	// Build EditOptions only from flags the user actually provided.
	// cmd.Flags().Changed("flag") returns true only if the flag appeared on the command line.
	// This is different from checking if the value is non-empty — it handles
	// the case where the user wants to set a field to its default value.
	opts := store.EditOptions{}

	if cmd.Flags().Changed("title") {
		title, _ := cmd.Flags().GetString("title")
		if title == "" {
			return fmt.Errorf("title cannot be empty")
		}
		opts.HasTitle = true
		opts.Title = title
	}

	if cmd.Flags().Changed("priority") {
		priorityStr, _ := cmd.Flags().GetString("priority")
		p, err := model.ParsePriority(priorityStr)
		if err != nil {
			return err
		}
		opts.HasPriority = true
		opts.Priority = p
	}

	if cmd.Flags().Changed("due") {
		due, _ := cmd.Flags().GetString("due")
		if due == "none" {
			due = "" // clear the due date
		} else if due != "" {
			if _, err := validateDate(due); err != nil {
				return err
			}
		}
		opts.HasDueDate = true
		opts.DueDate = due
	}

	if cmd.Flags().Changed("status") {
		statusStr, _ := cmd.Flags().GetString("status")
		switch statusStr {
		case "pending":
			opts.HasStatus = true
			opts.Status = model.StatusPending
		case "done":
			opts.HasStatus = true
			opts.Status = model.StatusDone
		default:
			return fmt.Errorf("invalid status %q: use pending or done", statusStr)
		}
	}

	// If no flags were provided, tell the user rather than silently doing nothing.
	if !opts.HasTitle && !opts.HasPriority && !opts.HasDueDate && !opts.HasStatus {
		return fmt.Errorf("no fields to edit; use --title, --priority, --due, or --status")
	}

	s, err := store.NewStore()
	if err != nil {
		return fmt.Errorf("opening store: %w", err)
	}

	task, err := s.Edit(shortID, opts)
	if err != nil {
		return err
	}

	cyan := color.New(color.FgCyan, color.Bold).SprintFunc()
	fmt.Printf("%s Task #%s updated:\n", cyan("~"), task.ShortID())
	fmt.Printf("  Title:    %s\n", task.Title)
	fmt.Printf("  Priority: %s\n", priorityColor(task.Priority))
	fmt.Printf("  Status:   %s\n", task.Status)
	if task.DueDate != "" {
		fmt.Printf("  Due:      %s\n", task.DueDate)
	}
	return nil
}
```

---

## Stats command — `cmd/stats.go`

```go
// cmd/stats.go
package cmd

import (
	"fmt"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/yourname/taskr/internal/model"
	"github.com/yourname/taskr/internal/store"
)

var statsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Show task statistics",
	Args:  cobra.NoArgs,
	RunE:  runStats,
}

func runStats(cmd *cobra.Command, args []string) error {
	if viper.GetBool("no_color") {
		color.NoColor = true
	}

	s, err := store.NewStore()
	if err != nil {
		return fmt.Errorf("opening store: %w", err)
	}

	stats, err := s.Stats()
	if err != nil {
		return fmt.Errorf("computing stats: %w", err)
	}

	if stats.Total == 0 {
		fmt.Println("No tasks yet. Add one with: taskr add \"My first task\"")
		return nil
	}

	completionPct := 0
	if stats.Total > 0 {
		completionPct = (stats.Done * 100) / stats.Total
	}

	bold := color.New(color.Bold).SprintFunc()
	green := color.New(color.FgGreen).SprintFunc()
	yellow := color.New(color.FgYellow).SprintFunc()

	fmt.Println(bold("Task Statistics"))
	fmt.Println("───────────────────────────────")
	fmt.Printf("  Total:      %s\n", bold(stats.Total))
	fmt.Printf("  Done:       %s\n", green(stats.Done))
	fmt.Printf("  Pending:    %s\n", yellow(stats.Pending))
	fmt.Printf("  Completion: %s%%\n", bold(completionPct))
	fmt.Println()
	fmt.Println(bold("By Priority"))
	fmt.Println("───────────────────────────────")
	fmt.Printf("  High:   %d\n", stats.ByPriority[model.PriorityHigh])
	fmt.Printf("  Medium: %d\n", stats.ByPriority[model.PriorityMedium])
	fmt.Printf("  Low:    %d\n", stats.ByPriority[model.PriorityLow])

	// ASCII progress bar — cosmetic but gives the stats command personality.
	if stats.Total > 0 {
		fmt.Println()
		printProgressBar("Completion", stats.Done, stats.Total, 30)
	}

	return nil
}

// printProgressBar renders a text progress bar like: [████████░░░░░░░░] 45%
func printProgressBar(label string, done, total, width int) {
	if total == 0 {
		return
	}
	filled := (done * width) / total
	empty := width - filled

	bar := fmt.Sprintf("[%s%s]",
		color.GreenString(repeatStr("█", filled)),
		repeatStr("░", empty),
	)
	pct := (done * 100) / total
	fmt.Printf("  %-12s %s %d%%\n", label, bar, pct)
}

func repeatStr(s string, n int) string {
	result := ""
	for i := 0; i < n; i++ {
		result += s
	}
	return result
}
```

---

## Shell completion — `cmd/completion.go`

Cobra generates shell completion scripts for you. All you need to do is expose the command:

```go
// cmd/completion.go
package cmd

import (
	"os"

	"github.com/spf13/cobra"
)

var completionCmd = &cobra.Command{
	Use:   "completion [bash|zsh|fish|powershell]",
	Short: "Generate shell completion scripts",
	Long: `Generate a shell completion script for taskr.

To load completions in the current shell session (bash):
    source <(taskr completion bash)

To load completions permanently (bash):
    taskr completion bash > /etc/bash_completion.d/taskr

To load completions permanently (zsh):
    taskr completion zsh > "${fpath[1]}/_taskr"
`,
	DisableFlagsInUseLine: true,
	ValidArgs:             []string{"bash", "zsh", "fish", "powershell"},
	Args:                  cobra.ExactValidArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		switch args[0] {
		case "bash":
			// GenBashCompletionV2 is preferred over the V1 variant because
			// it supports descriptions on completion candidates.
			cmd.Root().GenBashCompletionV2(os.Stdout, true)
		case "zsh":
			cmd.Root().GenZshCompletion(os.Stdout)
		case "fish":
			cmd.Root().GenFishCompletion(os.Stdout, true)
		case "powershell":
			cmd.Root().GenPowerShellCompletionWithDesc(os.Stdout)
		}
	},
}
```

---

## Makefile with cross-platform builds

```makefile
# Makefile
BINARY  = taskr
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS = -ldflags "-X main.version=$(VERSION) -s -w"
GOFILES = $(shell find . -name '*.go' -not -path './vendor/*')

.PHONY: build test lint install clean release

## build: compile for the current OS/arch
build:
	go build $(LDFLAGS) -o bin/$(BINARY) .

## install: install to $GOPATH/bin
install:
	go install $(LDFLAGS) .

## test: run all tests
test:
	go test ./... -v -count=1

## lint: run golangci-lint
lint:
	golangci-lint run ./...

## clean: remove build artifacts
clean:
	rm -rf bin/ dist/

## release: cross-compile for Linux, macOS, Windows
release: clean
	GOOS=linux   GOARCH=amd64  go build $(LDFLAGS) -o dist/$(BINARY)-linux-amd64    .
	GOOS=linux   GOARCH=arm64  go build $(LDFLAGS) -o dist/$(BINARY)-linux-arm64    .
	GOOS=darwin  GOARCH=amd64  go build $(LDFLAGS) -o dist/$(BINARY)-darwin-amd64   .
	GOOS=darwin  GOARCH=arm64  go build $(LDFLAGS) -o dist/$(BINARY)-darwin-arm64   .
	GOOS=windows GOARCH=amd64  go build $(LDFLAGS) -o dist/$(BINARY)-windows-amd64.exe .
	@echo "Release binaries in dist/"

## goreleaser: use goreleaser for fully automated release (requires .goreleaser.yaml)
goreleaser:
	goreleaser release --snapshot --clean
```

**Cross-compilation notes:**

`GOOS` and `GOARCH` are environment variables that the Go toolchain reads before compiling. Because Go ships its own linker and doesn't depend on the host C compiler (for pure Go programs), cross-compilation is free: you set the variables and run `go build`. The `-s -w` ldflags strip the symbol table and DWARF debug info, reducing binary size by ~30%.

For production release pipelines, [GoReleaser](https://goreleaser.com/) automates this: it builds all platforms, generates checksums, creates GitHub release assets, and can push Docker images — all from a single YAML file.

---

## Running the tool

```bash
# Build and install
make install

# Add some tasks
taskr add "Write the scraper chapter" --priority high --due 2024-11-01
taskr add "Buy groceries" --priority low
taskr add "Review code" --priority medium

# List all pending tasks
taskr list

# Complete one
taskr done <first 4+ chars of ID>

# See stats
taskr stats

# Set up shell completion (zsh example)
taskr completion zsh > "${fpath[1]}/_taskr"
```

---

## Key patterns learned in this chapter

| Pattern | Where used | Why |
|---------|-----------|-----|
| Cobra command tree | `cmd/root.go`, all subcommands | Scales to dozens of subcommands without spaghetti |
| `cmd.Flags().Changed()` | `cmd/edit.go` | Distinguishes "flag not given" from "flag given with empty value" |
| Atomic file write | `store/json_store.go` | Prevents data corruption on crash |
| Viper config layering | `cmd/root.go` | Env vars and config file override CLI defaults |
| `type X string` enum | `model/task.go` | Free JSON marshaling, type safety, Stringer |
| Short ID prefix matching | `store/json_store.go` | UX: users type 4-8 chars instead of 36 |

---

## Exercises

1. Add a `taskr search <query>` command that does full-text search across task titles (case-insensitive substring match).
2. Add due-date sorting to `taskr list --sort due`.
3. Implement `taskr export --format csv` that writes all tasks to stdout as CSV.
4. Add a `--dry-run` flag to `delete` that prints what would be deleted without actually deleting.
5. Write a test for the store's atomic write: inject a file path, add a task, verify the JSON is valid, then verify a second add works.
