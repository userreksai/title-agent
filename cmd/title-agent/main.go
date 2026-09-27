package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/userreksai/title-agent/internal/agent"
	"github.com/userreksai/title-agent/internal/settings"
)

func main() {
	path := flag.String("config", "agent.json", "JSON configuration path")
	check := flag.Bool("check-config", false, "validate config and exit")
	flag.Parse()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := settings.ReadAgent(*path)
	if err != nil {
		logger.Error("configuration", "error", err)
		os.Exit(1)
	}
	if *check {
		logger.Info("configuration valid")
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server := &http.Server{Addr: cfg.Listen, Handler: agent.New(cfg).Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: time.Duration(cfg.MaxTimeoutSeconds+5) * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	logger.Info("title agent listening", "address", cfg.Listen, "name", cfg.Name, "max_concurrent", cfg.MaxConcurrent)
	if err = server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("serve", "error", err)
		os.Exit(1)
	}
	<-shutdownDone
}
