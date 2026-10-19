package jobs

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"quotes/internal/metrics"

	dto "github.com/prometheus/client_model/go"
	"github.com/rs/zerolog"
)

func testLogger() *zerolog.Logger {
	nop := zerolog.Nop()
	return &nop
}

// lastSuccess reads the job_last_success_timestamp_seconds gauge for one job.
func lastSuccess(t *testing.T, job string) float64 {
	t.Helper()
	var m dto.Metric
	if err := metrics.JobLastSuccessTimestamp.WithLabelValues(job).Write(&m); err != nil {
		t.Fatalf("read gauge: %v", err)
	}
	return m.GetGauge().GetValue()
}

// TestTickFailureDoesNotAdvanceLastSuccess pins the metric's contract: the
// gauge answers "is the pipeline working", not "is the goroutine alive". A tick
// that reports failure must leave it untouched — stamping after every
// non-panicking tick kept it fresh through a total upstream outage, so the
// staleness alert its own doc prescribes could never fire.
func TestTickFailureDoesNotAdvanceLastSuccess(t *testing.T) {
	const job = "test-tick-failure"
	logger := testLogger()

	runTickWithCorrelation(context.Background(), time.Second, logger, job, func(context.Context) error {
		return nil
	})
	afterSuccess := lastSuccess(t, job)
	if afterSuccess == 0 {
		t.Fatal("a successful tick must stamp the gauge")
	}

	runTickWithCorrelation(context.Background(), time.Second, logger, job, func(context.Context) error {
		return errors.New("upstream down")
	})
	if got := lastSuccess(t, job); got != afterSuccess {
		t.Errorf("failed tick moved the gauge from %v to %v", afterSuccess, got)
	}
}

// A panicking tick must not stamp either — the recover keeps the loop alive,
// but nothing was collected.
func TestTickPanicDoesNotAdvanceLastSuccess(t *testing.T) {
	const job = "test-tick-panic"
	logger := testLogger()

	runTickWithCorrelation(context.Background(), time.Second, logger, job, func(context.Context) error {
		return nil
	})
	afterSuccess := lastSuccess(t, job)

	runTickWithCorrelation(context.Background(), time.Second, logger, job, func(context.Context) error {
		panic("boom")
	})
	if got := lastSuccess(t, job); got != afterSuccess {
		t.Errorf("panicking tick moved the gauge from %v to %v", afterSuccess, got)
	}
}

func TestTickOutcomeVerdict(t *testing.T) {
	live := context.Background()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	cases := []struct {
		name      string
		results   []error
		ctx       context.Context
		wantError bool
	}{
		{"all entities succeeded", []error{nil, nil}, live, false},
		{"partial success is progress", []error{errors.New("x"), nil}, live, false},
		{"every entity failed", []error{errors.New("x"), errors.New("y")}, live, true},
		{"no work to do", nil, live, false},
		{"cut short before starting", nil, cancelled, true},
		{"cut short after a success", []error{nil}, cancelled, false},

		// Skips abstain. They are permanent states — a backfill token that
		// reaches its floor is skipped on every tick forever — so counting one
		// as a success would let a single finished entity keep the gauge fresh
		// while everything still working is failing.
		{"only skips is a healthy idle tick", []error{errBackfillSkipped, errBackfillSkipped}, live, false},
		{"a skip must not outvote a failing sibling",
			[]error{errBackfillSkipped, errors.New("x")}, live, true},
		{"a skip alongside real work stays successful",
			[]error{errBackfillSkipped, nil}, live, false},
		{"an error cooldown is a failure, not a skip",
			[]error{errBackfillCoolingDown}, live, true},
		{"wrapped skips are recognised",
			[]error{fmt.Errorf("pair 7: %w", errBackfillSkipped)}, live, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out tickOutcome
			for _, err := range c.results {
				out.record(err)
			}
			got := out.verdict(c.ctx, "test tick")
			if (got != nil) != c.wantError {
				t.Errorf("verdict = %v, wantError = %v", got, c.wantError)
			}
		})
	}
}

// The tick context must actually carry the budget deadline — without this
// pin, deleting the WithTimeout block leaves the suite green.
func TestRunTick_AppliesDeadline(t *testing.T) {
	var hadDeadline bool
	runTickWithCorrelation(context.Background(), time.Second, testLogger(), "test-deadline", func(ctx context.Context) error {
		_, hadDeadline = ctx.Deadline()
		return nil
	})
	if !hadDeadline {
		t.Fatal("tick context must carry the budget deadline")
	}

	runTickWithCorrelation(context.Background(), 0, testLogger(), "test-no-deadline", func(ctx context.Context) error {
		_, hadDeadline = ctx.Deadline()
		return nil
	})
	if hadDeadline {
		t.Fatal("zero timeout must leave the tick context unbounded")
	}
}

func TestTickBudget(t *testing.T) {
	if got := tickBudget(10 * time.Second); got != 5*time.Minute {
		t.Errorf("short-interval budget = %v, want the 5m floor", got)
	}
	if got := tickBudget(time.Hour); got != 2*time.Hour {
		t.Errorf("long-interval budget = %v, want 2×interval", got)
	}
}

func TestDefaultJitter(t *testing.T) {
	if got := defaultJitter(10 * time.Second); got != time.Second {
		t.Errorf("defaultJitter(10s) = %v, want 1s", got)
	}
}
