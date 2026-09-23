package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"image-todo/internal/assets"
	"image-todo/internal/httpapi"
	"image-todo/internal/openai"
	"image-todo/internal/store"
	"image-todo/internal/worker"
	"image-todo/web"
)

func main() {
	dataDir := envOr("APP_DATA_DIR", "./data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		log.Fatalf("create data directory: %v", err)
	}

	database, err := store.Open(filepath.Join(dataDir, "app.sqlite"))
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer database.Close()

	imageStore, err := assets.New(filepath.Join(dataDir, "images"))
	if err != nil {
		log.Fatalf("open image storage: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	wake := make(chan struct{}, 1)
	imageClient := openai.New(os.Getenv("OPENAI_API_KEY"), envOr("OPENAI_IMAGE_MODEL", "gpt-image-2.5-sunburst")).WithAssets(imageStore)
	backgroundWorker := worker.New(database, imageStore, imageClient, wake)
	go backgroundWorker.Run(ctx)

	api := httpapi.New(database, imageStore, wake, os.Getenv("OPENAI_API_KEY") != "", web.Files)
	server := &http.Server{
		Addr:              envOr("APP_ADDR", "0.0.0.0:8080"),
		Handler:           api.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      6 * time.Minute,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	log.Printf("personal to-do server listening on %s", server.Addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("server failed: %v", err)
	}
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
