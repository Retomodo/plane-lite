package contract

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// RunJob runs a scheduled (Celery beat) task once and records it as a step
// (actor "job"). task is the Django task's dotted path, e.g.
// "plane.bgtasks.deletion_task.hard_delete". Recording runs it in a Django
// shell on the reference (Celery eager, so what it .delay()s runs too);
// verifying runs the Go periodic job named after the path's last part,
// with inline jobs. The step's body is "ok", or "error" when the task
// raised, which RunJob also returns.
func (s *Scenario) RunJob(task string) string {
	s.t.Helper()
	dot := strings.LastIndex(task, ".")
	if dot < 0 {
		s.t.Fatalf("RunJob: %q is not a dotted path", task)
	}
	module, fn := task[:dot], task[dot+1:]
	result := "ok"
	if s.tgt.env.record {
		code := fmt.Sprintf(`import importlib
try:
    importlib.import_module(%q).%s()
    print("JOB:ok")
except Exception as e:
    print("JOB:error", repr(e))
`, module, fn)
		cmd := exec.Command("../scripts/devstack.sh", strconv.Itoa(s.tgt.env.slot), "compose", "exec", "-T",
			"reference", "python", "manage.py", "shell", "-c", code)
		out, err := cmd.CombinedOutput()
		if err != nil {
			s.t.Fatalf("RunJob %s: %v\n%s", task, err, out)
		}
		switch {
		case strings.Contains(string(out), "JOB:ok"):
		case strings.Contains(string(out), "JOB:error"):
			result = "error"
			s.t.Logf("RunJob %s raised: %s", task, out)
		default:
			s.t.Fatalf("RunJob %s: no result\n%s", task, out)
		}
	} else if err := s.tgt.jobs.RunPeriodic(context.Background(), fn); err != nil {
		result = "error"
		s.t.Logf("RunJob %s: %v", task, err)
	}
	s.steps = append(s.steps, Step{Actor: "job", Method: "RUN", Path: task, Body: result})
	return result
}

// RunGoOnly runs fn against the Go server with no golden: for a deliberate
// deviation from Django (notes, DEVIATIONS.md), which no recording can
// cover. fn checks the outcome itself (s.DBStrings, t.Error). Skipped when
// recording.
func RunGoOnly(t *testing.T, fn func(s *Scenario)) {
	t.Helper()
	tgt := current(t)
	if tgt.env.record {
		t.Skip("Go-only test of a deliberate deviation: nothing to record")
	}
	if err := tgt.reset(context.Background()); err != nil {
		t.Fatalf("reset: %v", err)
	}
	fn(&Scenario{t: t, name: t.Name(), tgt: tgt, ids: newIDMap()})
}
