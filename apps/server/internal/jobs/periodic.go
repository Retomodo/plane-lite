package jobs

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/riverqueue/river"
	"github.com/robfig/cron/v3"
)

// Periodic jobs replace Celery beat (plane/celery.py's beat_schedule). The
// elected River leader enqueues them, so one replica runs each slot however
// many replicas there are.
//
// Each run is tied to its slot, the scheduled instant it stands for. A
// leader starting up enqueues the most recent slot of every schedule
// (RunOnStart), and River's unique jobs drop a slot that was already
// enqueued. So a process that slept through a slot (Cloudflare Containers
// sleep when idle) runs it once on waking, a restart doesn't repeat a slot
// that already ran, and older missed slots are skipped: every job is a
// sweep whose next run covers what a missed one would have done.

type periodic struct {
	spec  string
	sched cron.Schedule
	args  river.JobArgs
}

// periodicArgs is the queued form of a periodic job: which registered job
// to run and the slot it runs for. Both make it unique.
type periodicArgs struct {
	Job  string    `json:"job" river:"unique"`
	Slot time.Time `json:"slot" river:"unique"`
}

func (periodicArgs) Kind() string { return "periodic" }

// cronParser reads the five-field crontab syntax of Celery's crontab().
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// RegisterPeriodic runs args' job (registered with Register) on a cron
// schedule in UTC, like a Celery beat crontab entry. Call before Start.
func RegisterPeriodic(r *Runner, spec string, args river.JobArgs) {
	sched, err := cronParser.Parse("CRON_TZ=UTC " + spec)
	if err != nil {
		panic(fmt.Sprintf("jobs: schedule %q for %s: %v", spec, args.Kind(), err))
	}
	if _, dup := r.periodic[args.Kind()]; dup {
		panic("jobs: periodic job registered twice: " + args.Kind())
	}
	r.periodic[args.Kind()] = periodic{spec: spec, sched: sched, args: args}
}

// Periodic lists the registered periodic jobs' names, sorted.
func (r *Runner) Periodic() []string {
	var out []string
	for k := range r.periodic {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// RunPeriodic runs one periodic job now, in the caller's goroutine (plane
// run-job, tests). Jobs it enqueues follow the runner's mode.
func (r *Runner) RunPeriodic(ctx context.Context, name string) error {
	p, ok := r.periodic[name]
	if !ok {
		return fmt.Errorf("jobs: no periodic job %q (have %v)", name, r.Periodic())
	}
	return r.fns[p.args.Kind()](ctx, p.args)
}

// lastSlot is the latest instant of sched at or before now (the zero time
// if there is none within a year).
func lastSlot(sched cron.Schedule, now time.Time) time.Time {
	// Look back over a short window first: daily and shorter schedules
	// always have a slot in it.
	for _, back := range []time.Duration{48 * time.Hour, 32 * 24 * time.Hour, 367 * 24 * time.Hour} {
		var last time.Time
		for t := sched.Next(now.Add(-back)); !t.After(now); t = sched.Next(t) {
			last = t
		}
		if !last.IsZero() {
			return last
		}
	}
	return time.Time{}
}

// periodicJobs builds River's schedule entries.
func (r *Runner) periodicJobs() []*river.PeriodicJob {
	var out []*river.PeriodicJob
	for _, name := range r.Periodic() {
		p := r.periodic[name]
		out = append(out, river.NewPeriodicJob(p.sched, func() (river.JobArgs, *river.InsertOpts) {
			// River fires up to 100 ms early; the margin keeps a tick in
			// its own slot rather than the previous one.
			slot := lastSlot(p.sched, time.Now().UTC().Add(time.Second))
			return periodicArgs{Job: name, Slot: slot}, &river.InsertOpts{
				// Celery doesn't retry these tasks; the next slot is the retry.
				MaxAttempts: 1,
				UniqueOpts:  river.UniqueOpts{ByArgs: true},
			}
		}, &river.PeriodicJobOpts{ID: name, RunOnStart: true}))
	}
	return out
}

func (r *Runner) workPeriodic(ctx context.Context, job *river.Job[periodicArgs]) error {
	r.log.Info("periodic job", "job", job.Args.Job, "slot", job.Args.Slot)
	return r.RunPeriodic(ctx, job.Args.Job)
}
