// Package server wires configuration, storage and routes into an http.Handler.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"plane-lite/server/internal/api"
	"plane-lite/server/internal/auth"
	"plane-lite/server/internal/config"
	"plane-lite/server/internal/db"
	"plane-lite/server/internal/httpx"
	"plane-lite/server/internal/jobs"
	"plane-lite/server/internal/mail"
)

type Options struct {
	// InlineJobs runs background jobs synchronously inside the request, the
	// way the Django reference runs Celery in eager mode. Tests only.
	InlineJobs bool
}

type Server struct {
	handler http.Handler
	Pool    *pgxpool.Pool
	Redis   *redis.Client
	Jobs    *jobs.Runner
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

func New(ctx context.Context, cfg *config.Config, opts Options) (*Server, error) {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	pool, err := db.Open(ctx, cfg.DatabaseURL, cfg.DatabaseMaxConns)
	if err != nil {
		return nil, err
	}
	ropt, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		return nil, err
	}
	rdb := redis.NewClient(ropt)
	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("redis: %w", err)
	}

	sessions := auth.NewSessions(pool, cfg, log)
	runner := jobs.New(log, opts.InlineJobs)
	a := api.New(api.Deps{
		Config: cfg, DB: pool, Redis: rdb, Log: log, Sessions: sessions,
		Jobs: runner, Mailer: mail.New(cfg.Email),
	})
	if err := api.EnsureInstance(ctx, a); err != nil {
		return nil, fmt.Errorf("register instance: %w", err)
	}
	rt := httpx.NewRouter(log)
	a.Register(rt)

	var h http.Handler = rt
	h = sessions.Middleware(h)
	h = httpx.SecurityHeaders(h)
	h = httpx.CORS(cfg.CORSAllowedOrigins, h)
	h = httpx.Recover(log, h)
	h, err = withFrontend(h, cfg.StaticDir, cfg.LiveUpstream)
	if err != nil {
		return nil, err
	}
	return &Server{handler: h, Pool: pool, Redis: rdb, Jobs: runner}, nil
}

// Start begins background job processing.
func (s *Server) Start(ctx context.Context) error { return s.Jobs.Start(ctx, s.Pool) }

// Shutdown stops job processing and closes connections.
func (s *Server) Shutdown(ctx context.Context) error {
	err := s.Jobs.Stop(ctx)
	s.Pool.Close()
	_ = s.Redis.Close()
	return err
}
