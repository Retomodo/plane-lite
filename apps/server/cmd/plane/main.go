// Command plane runs the plane-lite API server.
//
//	plane serve     apply migrations, then serve HTTP (default)
//	plane migrate   apply migrations and exit
//
// For operators and tests, `plane run-job <name>` runs one periodic job once
// (e.g. hard_delete) with every job it enqueues run inline, then exits.
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
	"plane-lite/server/internal/jobs"
	"plane-lite/server/internal/server"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	if err := run(cmd, os.Args[min(2, len(os.Args)):], log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(cmd string, args []string, log *slog.Logger) error {
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
	if err == nil {
		err = jobs.Migrate(ctx, pool)
	}
	pool.Close()
	if err != nil {
		return err
	}

	switch cmd {
	case "migrate":
		log.Info("migrations applied")
		return nil
	case "serve":
	case "run-job":
		return runJob(ctx, cfg, args, log)
	default:
		return fmt.Errorf("unknown command %q (want serve or migrate)", cmd)
	}

	srv, err := server.New(ctx, cfg, server.Options{})
	if err != nil {
		return err
	}
	if err := srv.Start(ctx); err != nil {
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
	return errors.Join(hs.Shutdown(shutdownCtx), srv.Shutdown(shutdownCtx))
}

// runJob runs one periodic job now. Jobs it enqueues (issue activity, the
// digest's mails) run inline, as with Celery eager, so all its effects have
// landed when it returns.
func runJob(ctx context.Context, cfg *config.Config, args []string, log *slog.Logger) error {
	srv, err := server.New(ctx, cfg, server.Options{InlineJobs: true})
	if err != nil {
		return err
	}
	defer srv.Shutdown(context.WithoutCancel(ctx))
	if len(args) != 1 {
		return fmt.Errorf("usage: plane run-job <name>; jobs: %v", srv.Jobs.Periodic())
	}
	start := time.Now()
	if err := srv.Jobs.RunPeriodic(ctx, args[0]); err != nil {
		return err
	}
	log.Info("job done", "job", args[0], "took", time.Since(start).String())
	return nil
}
