package worker

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"image-todo/internal/assets"
	"image-todo/internal/store"
)

type Generator interface {
	Generate(context.Context, store.Task, store.Settings) ([]byte, error)
}

type Worker struct {
	store     *store.Store
	assets    *assets.Local
	generator Generator
	wake      <-chan struct{}
}

func New(database *store.Store, imageStore *assets.Local, generator Generator, wake <-chan struct{}) *Worker {
	return &Worker{store: database, assets: imageStore, generator: generator, wake: wake}
}

func (w *Worker) Run(ctx context.Context) {
	if err := w.store.RecoverInterrupted(ctx); err != nil {
		log.Printf("recover interrupted reward jobs: %v", err)
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		// Cancellation stops new claims, but an active request must finish so its
		// paid result can be saved before the process exits.
		jobCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 7*time.Minute)
		worked, err := w.processNext(jobCtx)
		cancel()
		if err != nil {
			log.Printf("reward worker: %v", err)
		}
		if worked {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-w.wake:
		case <-ticker.C:
		}
	}
}

func (w *Worker) processNext(ctx context.Context) (bool, error) {
	task, err := w.store.ClaimNextTask(ctx)
	if err != nil || task == nil {
		return false, err
	}

	settings, err := w.store.GetSettings(ctx)
	if err != nil {
		return true, w.fail(ctx, task.ID, fmt.Errorf("load character settings: %w", err))
	}
	hasCanonical := false
	for _, reference := range settings.References {
		if reference.IsCanonical {
			hasCanonical = true
			break
		}
	}
	if !hasCanonical {
		return true, w.fail(ctx, task.ID, fmt.Errorf("add a canonical reference image in settings before generating rewards"))
	}

	imageBytes, err := w.generator.Generate(ctx, *task, settings)
	if err != nil {
		return true, w.fail(ctx, task.ID, err)
	}
	relativePath := "rewards/" + task.ID + ".png"
	if err := w.assets.Save(relativePath, imageBytes); err != nil {
		return true, w.fail(ctx, task.ID, fmt.Errorf("save generated image: %w", err))
	}
	if err := w.store.MarkReady(ctx, task.ID, relativePath); err != nil {
		_ = w.assets.Delete(relativePath)
		return true, fmt.Errorf("save ready reward state: %w", err)
	}
	return true, nil
}

func (w *Worker) fail(ctx context.Context, id string, cause error) error {
	message := strings.TrimSpace(cause.Error())
	if len(message) > 400 {
		message = message[:400]
	}
	if err := w.store.MarkFailed(ctx, id, message); err != nil {
		return fmt.Errorf("%v; record failure: %w", cause, err)
	}
	log.Printf("reward for task %s failed: %s", id, message)
	return nil
}
