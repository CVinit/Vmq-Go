package main

import (
	"context"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"vmq/internal/app"
)

func main() {
	cfg := app.LoadConfig()
	if err := app.ValidateConfig(cfg); err != nil {
		log.Fatalf("invalid config: %v", err)
	}
	if err := app.WebRootExists(cfg.WebRoot); err != nil {
		log.Fatalf("invalid web root: %v", err)
	}

	store, err := app.NewPostgresStore(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("failed to connect database: %v", err)
	}
	defer store.Close()

	application, err := app.New(cfg, store)
	if err != nil {
		log.Fatalf("failed to initialize app: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	application.StartBackground(ctx)

	server := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           application.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	log.Printf("vmq go server listening on :%s", cfg.Port)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server failed: %v", err)
	}
}
