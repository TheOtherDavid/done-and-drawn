package main

import (
	"context"
	"errors"
	"log"
	"net"
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
	appAddr := envOr("APP_ADDR", "0.0.0.0:8080")
	allowedHosts, err := httpapi.ParseAllowedHosts(os.Getenv("APP_ALLOWED_HOSTS"))
	if err != nil {
		log.Fatal(err)
	}
	if host := boundHost(appAddr); host != "" {
		allowedHosts = append(allowedHosts, host)
	}

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
	workerCtx, stopWorker := context.WithCancel(context.Background())
	defer stopWorker()
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		backgroundWorker.Run(workerCtx)
	}()

	api := httpapi.New(database, imageStore, wake, os.Getenv("OPENAI_API_KEY") != "", web.Files, allowedHosts...)
	server := &http.Server{
		Addr:              appAddr,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      6 * time.Minute,
		IdleTimeout:       60 * time.Second,
	}

	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		stopWorker()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("server shutdown: %v", err)
		}
	}()

	log.Printf("Done and Drawn server listening on %s", server.Addr)
	serveErr := server.ListenAndServe()
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		stopWorker()
		<-workerDone
		log.Fatalf("server failed: %v", serveErr)
	}
	<-shutdownDone
	<-workerDone
}

func boundHost(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		return ""
	}
	return host
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
