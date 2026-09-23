package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("operation conflicts with current state")
)

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set sqlite busy timeout: %w", err)
	}
	if _, err := db.Exec(`PRAGMA journal_mode = WAL`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable sqlite WAL: %w", err)
	}
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func migrate(db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS tasks (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			completed_at TEXT,
			reward_status TEXT NOT NULL CHECK (reward_status IN ('none','queued','generating','ready','failed')),
			reward_image_path TEXT,
			reward_error TEXT,
			CHECK ((completed_at IS NULL AND reward_status = 'none') OR (completed_at IS NOT NULL AND reward_status <> 'none'))
		)`,
		`CREATE INDEX IF NOT EXISTS idx_tasks_completion ON tasks(completed_at, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_tasks_reward_queue ON tasks(reward_status, completed_at)`,
		`CREATE TABLE IF NOT EXISTS character_settings (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			character_prompt TEXT NOT NULL DEFAULT '',
			style_prompt TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS character_reference_images (
			id TEXT PRIMARY KEY,
			image_path TEXT NOT NULL,
			is_canonical INTEGER NOT NULL DEFAULT 0 CHECK (is_canonical IN (0,1)),
			created_at TEXT NOT NULL
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_one_canonical_reference ON character_reference_images(is_canonical) WHERE is_canonical = 1`,
		`INSERT OR IGNORE INTO character_settings (id, character_prompt, style_prompt, updated_at) VALUES (1, '', '', ?)`,
	}
	for _, statement := range statements {
		var err error
		if strings.HasPrefix(statement, "INSERT OR IGNORE") {
			_, err = db.Exec(statement, time.Now().UTC().Format(time.RFC3339Nano))
		} else {
			_, err = db.Exec(statement)
		}
		if err != nil {
			return fmt.Errorf("initialize database schema: %w", err)
		}
	}
	return nil
}

func (s *Store) CreateTask(ctx context.Context, title, description string) (Task, error) {
	id, err := NewID()
	if err != nil {
		return Task{}, err
	}
	now := time.Now().UTC()
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO tasks (id, title, description, created_at, reward_status) VALUES (?, ?, ?, ?, 'none')`,
		id, strings.TrimSpace(title), strings.TrimSpace(description), now.Format(time.RFC3339Nano))
	if err != nil {
		return Task{}, err
	}
	return Task{ID: id, Title: strings.TrimSpace(title), Description: strings.TrimSpace(description), CreatedAt: now, RewardStatus: RewardNone}, nil
}

func (s *Store) ListTasks(ctx context.Context) ([]Task, error) {
	rows, err := s.db.QueryContext(ctx, taskSelect+` ORDER BY CASE WHEN completed_at IS NULL THEN 0 ELSE 1 END, COALESCE(completed_at, created_at) DESC, created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tasks := make([]Task, 0)
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func (s *Store) GetTask(ctx context.Context, id string) (Task, error) {
	return scanTask(s.db.QueryRowContext(ctx, taskSelect+` WHERE id = ?`, id))
}

func (s *Store) UpdateTask(ctx context.Context, id, title, description string) (Task, error) {
	result, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET title = ?, description = ? WHERE id = ? AND completed_at IS NULL`,
		strings.TrimSpace(title), strings.TrimSpace(description), id)
	if err != nil {
		return Task{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return Task{}, err
	}
	if count == 0 {
		_, getErr := s.GetTask(ctx, id)
		if errors.Is(getErr, sql.ErrNoRows) {
			return Task{}, ErrNotFound
		}
		if getErr != nil {
			return Task{}, getErr
		}
		return Task{}, ErrConflict
	}
	return s.GetTask(ctx, id)
}

func (s *Store) DeleteTask(ctx context.Context, id string) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var rewardPath sql.NullString
	var completedAt sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT reward_image_path, completed_at FROM tasks WHERE id = ?`, id).Scan(&rewardPath, &completedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if completedAt.Valid {
		return "", ErrConflict
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM tasks WHERE id = ? AND completed_at IS NULL`, id)
	if err != nil {
		return "", err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return "", err
	}
	if changed == 0 {
		return "", ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return rewardPath.String, nil
}

func (s *Store) CompleteTask(ctx context.Context, id string) (Task, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, false, err
	}
	defer tx.Rollback()
	task, err := scanTask(tx.QueryRowContext(ctx, taskSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, false, ErrNotFound
	}
	if err != nil {
		return Task{}, false, err
	}
	if task.CompletedAt != nil {
		return task, false, nil
	}
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET completed_at = ?, reward_status = 'queued', reward_error = NULL WHERE id = ? AND completed_at IS NULL`, now.Format(time.RFC3339Nano), id); err != nil {
		return Task{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Task{}, false, err
	}
	task.CompletedAt = &now
	task.RewardStatus = RewardQueued
	return task, true, nil
}

func (s *Store) ClaimNextTask(ctx context.Context) (*Task, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM tasks WHERE reward_status = 'queued' ORDER BY completed_at, id LIMIT 1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE tasks SET reward_status = 'generating', reward_error = NULL WHERE id = ? AND reward_status = 'queued'`, id)
	if err != nil {
		return nil, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if changed == 0 {
		return nil, nil
	}
	task, err := scanTask(tx.QueryRowContext(ctx, taskSelect+` WHERE id = ?`, id))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &task, nil
}

func (s *Store) MarkReady(ctx context.Context, id, imagePath string) error {
	return updateRewardState(ctx, s.db, id, `UPDATE tasks SET reward_status = 'ready', reward_image_path = ?, reward_error = NULL WHERE id = ? AND reward_status = 'generating'`, imagePath, id)
}

func (s *Store) MarkFailed(ctx context.Context, id, message string) error {
	return updateRewardState(ctx, s.db, id, `UPDATE tasks SET reward_status = 'failed', reward_error = ?, reward_image_path = NULL WHERE id = ? AND reward_status = 'generating'`, message, id)
}

func (s *Store) RetryTask(ctx context.Context, id string) (Task, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE tasks SET reward_status = 'queued', reward_error = NULL WHERE id = ? AND completed_at IS NOT NULL AND reward_status = 'failed'`, id)
	if err != nil {
		return Task{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return Task{}, err
	}
	if changed == 0 {
		_, getErr := s.GetTask(ctx, id)
		if errors.Is(getErr, sql.ErrNoRows) {
			return Task{}, ErrNotFound
		}
		if getErr != nil {
			return Task{}, getErr
		}
		return Task{}, ErrConflict
	}
	return s.GetTask(ctx, id)
}

func (s *Store) RecoverInterrupted(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tasks SET reward_status = 'failed', reward_error = 'Generation was interrupted. Retry when ready.' WHERE reward_status = 'generating'`)
	return err
}

const taskSelect = `SELECT id, title, description, created_at, completed_at, reward_status, reward_image_path, reward_error FROM tasks`

type scanner interface{ Scan(...any) error }

func scanTask(row scanner) (Task, error) {
	var task Task
	var createdAt string
	var completedAt, imagePath, rewardError sql.NullString
	if err := row.Scan(&task.ID, &task.Title, &task.Description, &createdAt, &completedAt, &task.RewardStatus, &imagePath, &rewardError); err != nil {
		return Task{}, err
	}
	var err error
	task.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return Task{}, err
	}
	if completedAt.Valid {
		parsed, err := time.Parse(time.RFC3339Nano, completedAt.String)
		if err != nil {
			return Task{}, err
		}
		task.CompletedAt = &parsed
	}
	task.RewardImagePath = imagePath.String
	task.RewardError = rewardError.String
	return task, nil
}

func updateRewardState(ctx context.Context, db *sql.DB, id, query string, args ...any) error {
	result, err := db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return ErrConflict
	}
	return nil
}
