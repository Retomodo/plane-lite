// Command plane runs the plane-lite API server.
//
//	plane serve     apply migrations, then serve HTTP (default)
//	plane migrate   apply migrations and exit
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // user timezones must resolve in minimal containers

	"plane-lite/server/internal/config"
	"plane-lite/server/internal/db"
	"plane-lite/server/internal/server"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	if err := run(cmd, log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(cmd string, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	pool, err := db.Open(ctx, cfg.DatabaseURL, 2)
	if err != nil {
		return err
	}
	err = db.Migrate(ctx, pool)
	pool.Close()
	if err != nil {
		return err
	}

	switch cmd {
	case "migrate":
		log.Info("migrations applied")
		return nil
	case "serve":
	default:
		return fmt.Errorf("unknown command %q (want serve or migrate)", cmd)
	}

	srv, err := server.New(ctx, cfg, server.Options{})
	if err != nil {
		return err
	}
	hs := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Addr)
		errc <- hs.ListenAndServe()
	}()
	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return hs.Shutdown(shutdownCtx)
}
