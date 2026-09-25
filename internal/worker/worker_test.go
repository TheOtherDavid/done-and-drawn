package worker

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"image-todo/internal/assets"
	"image-todo/internal/store"
)

type fakeGenerator struct {
	calls    int
	task     store.Task
	settings store.Settings
	image    []byte
	err      error
}

func (f *fakeGenerator) Generate(_ context.Context, task store.Task, settings store.Settings) ([]byte, error) {
	f.calls++
	f.task = task
	f.settings = settings
	return f.image, f.err
}

func TestWorkerGeneratesOnlyForCompletedTasksAndCanRetry(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "tasks.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	images, err := assets.New(filepath.Join(root, "images"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SaveSettings(ctx, "red scarf", "jacket", "cottage", "small fox", "curious", "ink drawing"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddReference(ctx, "reference-1", "references/character.png"); err != nil {
		t.Fatal(err)
	}
	task, err := db.CreateTask(ctx, "Water plants", "Back patio")
	if err != nil {
		t.Fatal(err)
	}

	generator := &fakeGenerator{err: errors.New("temporary image error")}
	w := New(db, images, generator, nil)
	if worked, err := w.processNext(ctx); err != nil || worked || generator.calls != 0 {
		t.Fatalf("worker generated before completion: worked=%v calls=%d err=%v", worked, generator.calls, err)
	}
	if _, _, err := db.CompleteTask(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	if worked, err := w.processNext(ctx); err != nil || !worked || generator.calls != 1 {
		t.Fatalf("worker did not attempt completed task: worked=%v calls=%d err=%v", worked, generator.calls, err)
	}
	failed, err := db.GetTask(ctx, task.ID)
	if err != nil || failed.RewardStatus != store.RewardFailed || !strings.Contains(failed.RewardError, "temporary image error") {
		t.Fatalf("generation failure was not recorded: task=%+v err=%v", failed, err)
	}

	if _, err := db.RetryTask(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	generator.err = nil
	generator.image = []byte("generated image bytes")
	if worked, err := w.processNext(ctx); err != nil || !worked || generator.calls != 2 {
		t.Fatalf("worker did not retry failed task: worked=%v calls=%d err=%v", worked, generator.calls, err)
	}
	if generator.task.ID != task.ID || generator.settings.Appearance != "red scarf" || generator.settings.Clothing != "jacket" || generator.settings.Home != "cottage" || generator.settings.Companion != "small fox" || generator.settings.Personality != "curious" || generator.settings.ArtStyle != "ink drawing" || len(generator.settings.References) != 1 || !generator.settings.References[0].IsCanonical {
		t.Fatalf("generator received wrong task or character settings: task=%+v settings=%+v", generator.task, generator.settings)
	}
	ready, err := db.GetTask(ctx, task.ID)
	if err != nil || ready.RewardStatus != store.RewardReady || ready.RewardImagePath == "" {
		t.Fatalf("generated reward was not attached: task=%+v err=%v", ready, err)
	}
	file, err := images.Open(ready.RewardImagePath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	saved, err := io.ReadAll(file)
	if err != nil || !bytes.Equal(saved, generator.image) {
		t.Fatalf("saved reward differs from generated image: bytes=%q err=%v", saved, err)
	}
	if worked, err := w.processNext(ctx); err != nil || worked || generator.calls != 2 {
		t.Fatalf("ready reward was generated again: worked=%v calls=%d err=%v", worked, generator.calls, err)
	}
}

func TestWorkerRequiresCanonicalReference(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "tasks.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	images, err := assets.New(filepath.Join(root, "images"))
	if err != nil {
		t.Fatal(err)
	}
	task, err := db.CreateTask(ctx, "Water plants", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.CompleteTask(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	generator := &fakeGenerator{image: []byte("should not be used")}
	w := New(db, images, generator, nil)
	if worked, err := w.processNext(ctx); err != nil || !worked || generator.calls != 0 {
		t.Fatalf("worker called generator without a reference: worked=%v calls=%d err=%v", worked, generator.calls, err)
	}
	failed, err := db.GetTask(ctx, task.ID)
	if err != nil || failed.RewardStatus != store.RewardFailed || !strings.Contains(failed.RewardError, "canonical reference") {
		t.Fatalf("missing reference was not recorded: task=%+v err=%v", failed, err)
	}
}

type waitingGenerator struct {
	started chan struct{}
	release chan struct{}
	calls   int
}

func (g *waitingGenerator) Generate(ctx context.Context, _ store.Task, _ store.Settings) ([]byte, error) {
	g.calls++
	close(g.started)
	select {
	case <-g.release:
		return []byte("generated image"), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestWorkerDrainsCurrentGenerationOnShutdown(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "tasks.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	images, err := assets.New(filepath.Join(root, "images"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddReference(ctx, "reference-1", "references/character.png"); err != nil {
		t.Fatal(err)
	}
	first, err := db.CreateTask(ctx, "First", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.CreateTask(ctx, "Second", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range []store.Task{first, second} {
		if _, _, err := db.CompleteTask(ctx, task.ID); err != nil {
			t.Fatal(err)
		}
	}
	generator := &waitingGenerator{started: make(chan struct{}), release: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		New(db, images, generator, nil).Run(ctx)
	}()
	select {
	case <-generator.started:
	case <-time.After(2 * time.Second):
		t.Fatal("generation did not start")
	}
	stop()
	select {
	case <-done:
		t.Fatal("worker stopped before saving the active generation")
	case <-time.After(30 * time.Millisecond):
	}
	close(generator.release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not stop after active generation finished")
	}
	firstResult, err := db.GetTask(context.Background(), first.ID)
	if err != nil || firstResult.RewardStatus != store.RewardReady {
		t.Fatalf("active reward was not saved: task=%+v err=%v", firstResult, err)
	}
	secondResult, err := db.GetTask(context.Background(), second.ID)
	if err != nil || secondResult.RewardStatus != store.RewardQueued || generator.calls != 1 {
		t.Fatalf("worker claimed another reward during shutdown: task=%+v calls=%d err=%v", secondResult, generator.calls, err)
	}
}
