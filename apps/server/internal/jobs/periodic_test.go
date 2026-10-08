package jobs

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestLastSlot(t *testing.T) {
	now := time.Date(2026, 10, 8, 2, 50, 30, 0, time.UTC)
	for spec, want := range map[string]string{
		"*/5 * * * *": "2026-10-08T02:50:00Z",
		"0 0 * * *":   "2026-10-08T00:00:00Z",
		"0 3 * * *":   "2026-10-07T03:00:00Z",
		"45 2 * * *":  "2026-10-08T02:45:00Z",
		"0 0 1 1 *":   "2026-01-01T00:00:00Z",
	} {
		sched, err := cronParser.Parse("CRON_TZ=UTC " + spec)
		if err != nil {
			t.Fatal(err)
		}
		if got := lastSlot(sched, now).Format(time.RFC3339); got != want {
			t.Errorf("%s: got %s, want %s", spec, got, want)
		}
	}
	// Exactly on a slot, that slot.
	sched, _ := cronParser.Parse("CRON_TZ=UTC 0 1 * * *")
	at := time.Date(2026, 10, 8, 1, 0, 0, 0, time.UTC)
	if got := lastSlot(sched, at); !got.Equal(at) {
		t.Errorf("on the slot: got %s", got)
	}
}

type testPeriodicJob struct{}

func (testPeriodicJob) Kind() string { return "test_periodic" }

func TestRegisterPeriodic(t *testing.T) {
	r := New(slog.New(slog.NewTextHandler(io.Discard, nil)), true)
	var runs int
	Register(r, func(context.Context, testPeriodicJob) error { runs++; return nil })
	RegisterPeriodic(r, "0 1 * * *", testPeriodicJob{})
	if got := r.Periodic(); len(got) != 1 || got[0] != "test_periodic" {
		t.Fatalf("Periodic() = %v", got)
	}
	if err := r.RunPeriodic(context.Background(), "test_periodic"); err != nil || runs != 1 {
		t.Fatalf("RunPeriodic: %v, runs %d", err, runs)
	}
	if err := r.RunPeriodic(context.Background(), "nope"); err == nil {
		t.Fatal("RunPeriodic of an unknown job succeeded")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("bad spec did not panic")
		}
	}()
	RegisterPeriodic(New(r.log, true), "61 * * * *", testPeriodicJob{})
}

// testPool opens a database of its own on the CONTRACT_SLOT stack (the
// contract tests truncate plane_test, River's tables included).
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	slot, _ := strconv.Atoi(os.Getenv("CONTRACT_SLOT"))
	base := fmt.Sprintf("postgres://plane:plane@localhost:%d/", 55432+slot*10)
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, base+"plane")
	if err != nil {
		t.Skipf("no database (scripts/devstack.sh N up): %v", err)
	}
	_, _ = admin.Exec(ctx, "CREATE DATABASE plane_jobs_test")
	admin.Close(ctx)
	pool, err := pgxpool.New(ctx, base+"plane_jobs_test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE river_job, river_leader"); err != nil {
		t.Fatal(err)
	}
	return pool
}

// TestPeriodicCatchUp: a leader starting up runs the last slot it missed,
// once; a restart within the same slot doesn't run it again.
func TestPeriodicCatchUp(t *testing.T) {
	pool := testPool(t)
	var runs atomic.Int32
	start := func() *Runner {
		r := New(slog.New(slog.NewTextHandler(io.Discard, nil)), false)
		Register(r, func(context.Context, testPeriodicJob) error { runs.Add(1); return nil })
		RegisterPeriodic(r, "0 3 * * *", testPeriodicJob{})
		if err := r.Start(context.Background(), pool); err != nil {
			t.Fatal(err)
		}
		return r
	}
	wait := func(cond func() bool) bool {
		for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
			if cond() {
				return true
			}
		}
		return false
	}
	countJobs := func() int {
		var n int
		_ = pool.QueryRow(context.Background(), `SELECT count(*) FROM river_job WHERE kind = 'periodic'`).Scan(&n)
		return n
	}

	r := start()
	if !wait(func() bool { return runs.Load() == 1 }) {
		t.Fatalf("missed slot not run on start: %d runs", runs.Load())
	}
	if err := r.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	r = start()
	defer r.Stop(context.Background())
	// The new leader's RunOnStart insert is skipped as a duplicate; give it
	// time to be elected and try.
	var leader bool
	wait(func() bool {
		_ = pool.QueryRow(context.Background(), `SELECT count(*) > 0 FROM river_leader`).Scan(&leader)
		return leader
	})
	time.Sleep(2 * time.Second)
	if !leader {
		t.Fatal("second runner never became leader")
	}
	if n := runs.Load(); n != 1 {
		t.Fatalf("slot ran %d times", n)
	}
	if n := countJobs(); n != 1 {
		t.Fatalf("%d periodic jobs queued", n)
	}
}
