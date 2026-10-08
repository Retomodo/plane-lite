// Package jobs runs Plane's background tasks (the Celery tasks of the Django
// backend) on a Postgres-backed queue (River), inside the server process.
//
// Postgres rather than Redis: the deployment shares a Redis instance that
// may evict keys under memory pressure, and a Postgres queue can take jobs
// in the same transaction as the change that caused them.
package jobs

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

type Runner struct {
	log      *slog.Logger
	inline   bool
	workers  *river.Workers
	fns      map[string]func(context.Context, river.JobArgs) error
	periodic map[string]periodic
	client   *river.Client[pgx.Tx]
}

// New returns a runner. With inline set, Enqueue runs the job immediately in
// the caller's goroutine, the way the Django reference runs Celery eagerly
// (tests only).
func New(log *slog.Logger, inline bool) *Runner {
	r := &Runner{
		log:      log,
		inline:   inline,
		workers:  river.NewWorkers(),
		fns:      map[string]func(context.Context, river.JobArgs) error{},
		periodic: map[string]periodic{},
	}
	river.AddWorker(r.workers, river.WorkFunc(r.workPeriodic))
	return r
}

// Register adds the worker for one job type. Call before Start.
func Register[T river.JobArgs](r *Runner, fn func(context.Context, T) error) {
	var zero T
	r.fns[zero.Kind()] = func(ctx context.Context, args river.JobArgs) error { return fn(ctx, args.(T)) }
	river.AddWorker(r.workers, river.WorkFunc(func(ctx context.Context, job *river.Job[T]) error {
		return fn(ctx, job.Args)
	}))
}

// Start begins working jobs and, on the elected leader, scheduling the
// periodic ones. A no-op in inline mode.
func (r *Runner) Start(ctx context.Context, pool *pgxpool.Pool) error {
	if r.inline {
		return nil
	}
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Logger:       r.log,
		Queues:       map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 10}},
		Workers:      r.workers,
		PeriodicJobs: r.periodicJobs(),
	})
	if err != nil {
		return fmt.Errorf("jobs: %w", err)
	}
	r.client = client
	return client.Start(ctx)
}

// Stop waits for running jobs to finish (or ctx to expire).
func (r *Runner) Stop(ctx context.Context) error {
	if r.client == nil {
		return nil
	}
	return r.client.Stop(ctx)
}

// Enqueue schedules a job. Like a Celery .delay() whose task swallows its
// own errors, an inline job's failure is logged rather than returned.
func Enqueue(ctx context.Context, r *Runner, args river.JobArgs) error {
	if r.inline {
		fn, ok := r.fns[args.Kind()]
		if !ok {
			return fmt.Errorf("jobs: no worker for %q", args.Kind())
		}
		if err := fn(ctx, args); err != nil {
			r.log.Error("job failed", "kind", args.Kind(), "err", err)
		}
		return nil
	}
	if r.client == nil {
		return fmt.Errorf("jobs: runner not started")
	}
	_, err := r.client.Insert(ctx, args, nil)
	return err
}

// Migrate creates or upgrades River's tables.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	m, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return err
	}
	_, err = m.Migrate(ctx, rivermigrate.DirectionUp, nil)
	return err
}
