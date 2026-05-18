// Package wake provides cross-platform detection of system
// suspend/wake-from-sleep events via a wall-clock-jump heuristic.
//
// When the OS suspends the process (laptop lid close, systemctl
// suspend, etc.), all goroutines pause and wall-clock time advances
// without the process observing it. On wake, time.Now() jumps forward
// by the sleep duration. The Detector schedules periodic checks via
// a ticker; if the actual elapsed wall-clock time between checks
// exceeds the expected interval plus a small threshold, the gap is
// reported via the onWake callback.
//
// This is intentionally cross-platform and dependency-free. It does
// not use D-Bus (Linux) or IOKit (macOS), which would require either
// platform-specific code or CGO. The clock-jump heuristic is a small
// generality loss — a brief, deliberate `date` change on Linux would
// also trigger it — in exchange for one implementation that works
// everywhere.
//
// Typical usage:
//
//	d := wake.New(10*time.Second, 5*time.Second, func(elapsed time.Duration) {
//	    log.Printf("system slept for ~%v, triggering catch-up", elapsed)
//	    // ... reconcile state with the server here
//	})
//	go d.Run(ctx)
package wake

import (
	"context"
	"time"
)

// Detector observes wall-clock time at fixed intervals and reports
// jumps larger than the expected interval. Construct with New; drive
// with Run (production) or Step (tests).
type Detector struct {
	interval  time.Duration
	threshold time.Duration
	onWake    func(elapsed time.Duration)

	// now is the wall-clock source. Defaults to time.Now in New;
	// tests override the field directly to inject a controlled clock.
	now func() time.Time

	// initialized is false until the first Step seeds last.
	initialized bool
	// last is the most recent observation. Only meaningful when
	// initialized.
	last time.Time
}

// New constructs a Detector. interval is the expected gap between
// observations; threshold is the slack we allow before declaring a
// jump (so normal scheduling jitter doesn't false-positive). onWake
// is invoked synchronously on the goroutine that calls Step (or Run's
// goroutine) when a jump is observed; it must be cheap or dispatch
// the actual work to another goroutine.
//
// The zero Detector is not usable; always construct via New.
func New(interval, threshold time.Duration, onWake func(time.Duration)) *Detector {
	return &Detector{
		interval:  interval,
		threshold: threshold,
		onWake:    onWake,
		now:       time.Now,
	}
}

// Step records a single clock observation. The first call seeds the
// baseline; subsequent calls compute elapsed-since-previous and fire
// onWake if it exceeds interval+threshold.
//
// Exported for tests so the logic can be exercised without spawning
// a goroutine and without relying on a real ticker. Run calls this
// internally on every tick.
func (d *Detector) Step() {
	now := d.now()
	if !d.initialized {
		d.last = now
		d.initialized = true
		return
	}
	elapsed := now.Sub(d.last)
	d.last = now
	if elapsed > d.interval+d.threshold {
		d.onWake(elapsed)
	}
}

// Run drives Step on a ticker until ctx is cancelled. Blocks; intended
// to be invoked in a goroutine.
//
// The first Step happens immediately to seed the baseline; subsequent
// Steps happen on every tick of interval. On ctx cancellation Run
// returns promptly without firing further callbacks.
func (d *Detector) Run(ctx context.Context) {
	d.Step()
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.Step()
		}
	}
}
