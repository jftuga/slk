package wake

import (
	"context"
	"sync"
	"testing"
	"time"
)

// fakeClock returns whatever its current field says. Tests advance
// the clock via Advance or Set. The mutex is necessary for the
// TestRun_* tests where the detector goroutine reads concurrently
// with the test goroutine; serialized Step tests don't strictly need
// it but pay nothing for it.
type fakeClock struct {
	mu      sync.Mutex
	current time.Time
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.current
}

func (f *fakeClock) Set(t time.Time) {
	f.mu.Lock()
	f.current = t
	f.mu.Unlock()
}

func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	f.current = f.current.Add(d)
	f.mu.Unlock()
}

func TestNew_DefaultsToRealClock(t *testing.T) {
	d := New(time.Second, time.Second, func(time.Duration) {})
	if d.now == nil {
		t.Fatal("New should default now to a non-nil function")
	}
	t1 := d.now()
	time.Sleep(time.Millisecond)
	t2 := d.now()
	if !t2.After(t1) {
		t.Errorf("now() did not advance — not wired to real clock? t1=%v t2=%v", t1, t2)
	}
}

func TestStep_NoJump_NoCallback(t *testing.T) {
	fc := &fakeClock{current: time.Unix(1000, 0)}
	fired := 0
	d := &Detector{
		interval:  10 * time.Second,
		threshold: 5 * time.Second,
		now:       fc.Now,
		onWake:    func(time.Duration) { fired++ },
	}
	d.Step() // seed baseline at t=1000
	fc.Advance(10 * time.Second)
	d.Step() // elapsed = 10s, not > 15s threshold
	if fired != 0 {
		t.Errorf("callback fired %d times on normal tick; want 0", fired)
	}
}

func TestStep_JumpAboveThreshold_FiresCallback(t *testing.T) {
	fc := &fakeClock{current: time.Unix(1000, 0)}
	var got time.Duration
	fired := 0
	d := &Detector{
		interval:  10 * time.Second,
		threshold: 5 * time.Second,
		now:       fc.Now,
		onWake:    func(e time.Duration) { fired++; got = e },
	}
	d.Step()
	fc.Advance(2 * time.Minute)
	d.Step()
	if fired != 1 {
		t.Errorf("fired = %d, want 1", fired)
	}
	if got != 2*time.Minute {
		t.Errorf("elapsed = %v, want 2m", got)
	}
}

func TestStep_JumpBelowThreshold_NoCallback(t *testing.T) {
	fc := &fakeClock{current: time.Unix(1000, 0)}
	fired := 0
	d := &Detector{
		interval:  10 * time.Second,
		threshold: 5 * time.Second,
		now:       fc.Now,
		onWake:    func(time.Duration) { fired++ },
	}
	d.Step()
	// 14s is greater than interval (10s) but less than interval+threshold (15s).
	// This is the "scheduling jitter" zone we explicitly tolerate.
	fc.Advance(14 * time.Second)
	d.Step()
	if fired != 0 {
		t.Errorf("callback fired %d times in jitter zone; want 0", fired)
	}
}

func TestStep_ExactlyAtThreshold_NoCallback(t *testing.T) {
	// Boundary: elapsed == interval+threshold uses strict >. A jump of
	// exactly the threshold should NOT fire; only strictly larger.
	fc := &fakeClock{current: time.Unix(1000, 0)}
	fired := 0
	d := &Detector{
		interval:  10 * time.Second,
		threshold: 5 * time.Second,
		now:       fc.Now,
		onWake:    func(time.Duration) { fired++ },
	}
	d.Step()
	fc.Advance(15 * time.Second)
	d.Step()
	if fired != 0 {
		t.Errorf("callback fired %d times at exact threshold; want 0 (strict >)", fired)
	}
}

func TestStep_OneNanosecondAboveThreshold_FiresCallback(t *testing.T) {
	// Companion to TestStep_ExactlyAtThreshold: one ns over the
	// boundary must fire. Together these tests pin down the strict-> contract.
	fc := &fakeClock{current: time.Unix(1000, 0)}
	fired := 0
	d := &Detector{
		interval:  10 * time.Second,
		threshold: 5 * time.Second,
		now:       fc.Now,
		onWake:    func(time.Duration) { fired++ },
	}
	d.Step()
	fc.Advance(15*time.Second + 1)
	d.Step()
	if fired != 1 {
		t.Errorf("callback fired %d times at threshold+1ns; want 1", fired)
	}
}

func TestStep_MultipleJumps_FireEach(t *testing.T) {
	fc := &fakeClock{current: time.Unix(1000, 0)}
	var elapsed []time.Duration
	d := &Detector{
		interval:  10 * time.Second,
		threshold: 5 * time.Second,
		now:       fc.Now,
		onWake:    func(e time.Duration) { elapsed = append(elapsed, e) },
	}
	d.Step() // seed
	fc.Advance(60 * time.Second)
	d.Step() // jump 1: 60s
	fc.Advance(120 * time.Second)
	d.Step() // jump 2: 120s
	if len(elapsed) != 2 {
		t.Fatalf("fired %d times, want 2: %v", len(elapsed), elapsed)
	}
	if elapsed[0] != 60*time.Second {
		t.Errorf("first jump = %v, want 60s", elapsed[0])
	}
	if elapsed[1] != 120*time.Second {
		t.Errorf("second jump = %v, want 120s", elapsed[1])
	}
}

func TestStep_NormalTickAfterJump_DoesNotRefire(t *testing.T) {
	// After a jump fires, the next normal tick should NOT re-fire
	// (the baseline `last` must have advanced to the post-jump time).
	fc := &fakeClock{current: time.Unix(1000, 0)}
	fired := 0
	d := &Detector{
		interval:  10 * time.Second,
		threshold: 5 * time.Second,
		now:       fc.Now,
		onWake:    func(time.Duration) { fired++ },
	}
	d.Step() // seed
	fc.Advance(60 * time.Second)
	d.Step() // fires
	if fired != 1 {
		t.Fatalf("expected fire on jump")
	}
	fc.Advance(10 * time.Second)
	d.Step() // normal tick, must not fire
	if fired != 1 {
		t.Errorf("callback re-fired on normal tick after jump; baseline not advanced")
	}
}

func TestStep_FirstCallSeedsWithoutFiring(t *testing.T) {
	// The very first Step seeds last; even if `now` returns wildly
	// in the future, no callback should fire because there's no
	// baseline to compare to.
	fc := &fakeClock{current: time.Unix(9_999_999, 0)}
	fired := 0
	d := &Detector{
		interval:  10 * time.Second,
		threshold: 5 * time.Second,
		now:       fc.Now,
		onWake:    func(time.Duration) { fired++ },
	}
	d.Step()
	if fired != 0 {
		t.Errorf("callback fired %d times on first Step; want 0 (seeding only)", fired)
	}
	if !d.initialized {
		t.Errorf("first Step did not set initialized=true")
	}
	if !d.last.Equal(fc.Now()) {
		t.Errorf("first Step did not store baseline; last=%v want=%v", d.last, fc.Now())
	}
}

func TestRun_ContextCancellation_ReturnsCleanly(t *testing.T) {
	// Drive Run with a real ticker for a short period, then cancel.
	// Confirm the goroutine exits within a generous timeout.
	d := New(10*time.Millisecond, 5*time.Millisecond, func(time.Duration) {})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		d.Run(ctx)
		close(done)
	}()
	// Let the loop spin a few times.
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
		// success
	case <-time.After(time.Second):
		t.Fatal("Run did not return within 1s after ctx cancel")
	}
}

func TestRun_FiresOnSimulatedJump(t *testing.T) {
	// Integration check that Run actually calls Step on every tick.
	// We can't synchronize a real ticker with manual fake-clock
	// advances without races, so we keep the test scope narrow:
	// confirm that *after* simulating a large clock jump on the fake
	// clock, the callback eventually fires. The Step tests cover the
	// exact-threshold and below-threshold cases — this test only
	// proves Run wires Step to the ticker.
	fc := &fakeClock{current: time.Unix(1000, 0)}
	fired := make(chan time.Duration, 10)
	d := New(5*time.Millisecond, 5*time.Millisecond, func(e time.Duration) {
		fired <- e
	})
	d.now = fc.Now

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)

	// Give Run a moment to seed the baseline.
	time.Sleep(20 * time.Millisecond)

	// Simulate a wake: advance the fake clock by 2 seconds. The next
	// tick (within ~5ms) calls Step, observes the jump, fires onWake.
	fc.Advance(2 * time.Second)
	select {
	case e := <-fired:
		if e < 2*time.Second {
			t.Errorf("callback elapsed = %v, want >= 2s", e)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("callback not fired within 500ms of simulated jump")
	}
}
