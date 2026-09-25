package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTaskRewardLifecycle(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "tasks.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	task, err := db.CreateTask(ctx, "Water plants", "Back patio")
	if err != nil {
		t.Fatal(err)
	}
	if task.RewardStatus != RewardNone || task.CompletedAt != nil {
		t.Fatalf("new task should have no reward: %+v", task)
	}
	if next, err := db.ClaimNextTask(ctx); err != nil || next != nil {
		t.Fatalf("unfinished task entered reward queue: next=%+v err=%v", next, err)
	}

	completed, queued, err := db.CompleteTask(ctx, task.ID)
	if err != nil || !queued || completed.RewardStatus != RewardQueued || completed.CompletedAt == nil {
		t.Fatalf("completion did not queue reward: task=%+v queued=%v err=%v", completed, queued, err)
	}
	completedAgain, queuedAgain, err := db.CompleteTask(ctx, task.ID)
	if err != nil || queuedAgain || completedAgain.RewardStatus != RewardQueued {
		t.Fatalf("repeat completion queued another reward: task=%+v queued=%v err=%v", completedAgain, queuedAgain, err)
	}
	if err := db.MarkReady(ctx, task.ID, "rewards/early.png"); !errors.Is(err, ErrConflict) {
		t.Fatalf("queued task became ready without generation: %v", err)
	}

	claimed, err := db.ClaimNextTask(ctx)
	if err != nil || claimed == nil || claimed.ID != task.ID || claimed.RewardStatus != RewardGenerating {
		t.Fatalf("could not claim queued reward: task=%+v err=%v", claimed, err)
	}
	if next, err := db.ClaimNextTask(ctx); err != nil || next != nil {
		t.Fatalf("claimed reward remained queued: next=%+v err=%v", next, err)
	}
	if err := db.MarkFailed(ctx, task.ID, "generation failed"); err != nil {
		t.Fatal(err)
	}
	failed, err := db.GetTask(ctx, task.ID)
	if err != nil || failed.RewardStatus != RewardFailed || failed.RewardError != "generation failed" {
		t.Fatalf("failure state was not saved: task=%+v err=%v", failed, err)
	}

	retried, err := db.RetryTask(ctx, task.ID)
	if err != nil || retried.RewardStatus != RewardQueued || retried.RewardError != "" {
		t.Fatalf("retry did not requeue task: task=%+v err=%v", retried, err)
	}
	claimed, err = db.ClaimNextTask(ctx)
	if err != nil || claimed == nil || claimed.RewardStatus != RewardGenerating {
		t.Fatalf("retried reward was not claimable: task=%+v err=%v", claimed, err)
	}
	if err := db.MarkReady(ctx, task.ID, "rewards/final.png"); err != nil {
		t.Fatal(err)
	}
	ready, err := db.GetTask(ctx, task.ID)
	if err != nil || ready.RewardStatus != RewardReady || ready.RewardImagePath != "rewards/final.png" {
		t.Fatalf("ready reward was not saved: task=%+v err=%v", ready, err)
	}
	if _, err := db.RetryTask(ctx, task.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("ready reward accepted retry: %v", err)
	}
	if _, err := db.DeleteTask(ctx, task.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("completed task was deleted: %v", err)
	}
}

func TestRewardQueueSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "tasks.sqlite")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	task, err := first.CreateTask(ctx, "Clean desk", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := first.CompleteTask(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := second.ClaimNextTask(ctx)
	if err != nil || claimed == nil || claimed.ID != task.ID {
		t.Fatalf("queued reward was lost after restart: task=%+v err=%v", claimed, err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}

	third, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer third.Close()
	if err := third.RecoverInterrupted(ctx); err != nil {
		t.Fatal(err)
	}
	recovered, err := third.GetTask(ctx, task.ID)
	if err != nil || recovered.RewardStatus != RewardFailed || !strings.Contains(recovered.RewardError, "may have been charged") {
		t.Fatalf("interrupted generation was not recoverable: task=%+v err=%v", recovered, err)
	}
	if _, err := third.RetryTask(ctx, task.ID); err != nil {
		t.Fatalf("recovered reward could not be retried: %v", err)
	}
}

func TestReferenceCountLimit(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "references.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for i := range MaxReferenceImages {
		id := fmt.Sprintf("reference-%d", i)
		if _, err := db.AddReference(ctx, id, "references/"+id+".png"); err != nil {
			t.Fatalf("add reference %d: %v", i+1, err)
		}
	}
	if _, err := db.AddReference(ctx, "reference-extra", "references/extra.png"); !errors.Is(err, ErrReferenceLimit) {
		t.Fatalf("17th reference should be rejected: %v", err)
	}
	settings, err := db.GetSettings(ctx)
	if err != nil || len(settings.References) != MaxReferenceImages {
		t.Fatalf("reference count changed after rejection: count=%d err=%v", len(settings.References), err)
	}
}

func TestLegacyCharacterPromptsMigrateOnce(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.sqlite")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`CREATE TABLE character_settings (
		id INTEGER PRIMARY KEY,
		character_prompt TEXT NOT NULL DEFAULT '',
		style_prompt TEXT NOT NULL DEFAULT '',
		updated_at TEXT NOT NULL
	)`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`INSERT INTO character_settings (id, character_prompt, style_prompt, updated_at) VALUES (1, 'old appearance', 'old style', ?)`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := db.GetSettings(ctx)
	if err != nil || settings.Appearance != "old appearance" || settings.ArtStyle != "old style" || settings.Clothing != "" || settings.Home != "" || settings.Companion != "" || settings.Personality != "" {
		t.Fatalf("legacy character prompts were not preserved: settings=%+v err=%v", settings, err)
	}
	if _, err := db.SaveSettings(ctx, "", "", "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	settings, err = reopened.GetSettings(ctx)
	if err != nil || settings.Appearance != "" || settings.ArtStyle != "" {
		t.Fatalf("cleared migrated prompts were restored on next startup: settings=%+v err=%v", settings, err)
	}
}
