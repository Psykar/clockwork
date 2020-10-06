package clockwork

import (
	"errors"
	"sync"
	"time"
)

// Clock provides an interface that packages can use instead of directly
// using the time module, so that chronology-related behavior can be tested
type Clock interface {
	After(d time.Duration) <-chan time.Time
	At(t time.Time) <-chan time.Time

	Sleep(d time.Duration)
	Now() time.Time
	Since(t time.Time) time.Duration
	NewTicker(d time.Duration) Ticker
}

// FakeClock provides an interface for a clock which can be
// manually advanced through time
type FakeClock interface {
	Clock
	// Advance advances the FakeClock to a new point in time, ensuring any existing
	// sleepers are notified appropriately before returning
	Advance(d time.Duration)

	// AdvanceTo advances the FakeClock to a new point in time, where the time
	// is specified directly. Returns false if the specified time has allready
	// passed.
	AdvanceTo(t time.Time) bool

	// NextWakeup returns the earliest time from now that a blocker would
	// wait up. Returns a Zero time if there are no more blockers.
	//
	// This can be used with Advance to wake subsequent blockers no matter
	// what time they are blocked on.
	NextWakeup() time.Time

	// BlockUntil will block until the FakeClock has the given number of
	// sleepers (callers of Sleep or After)
	BlockUntil(n int)

	// NumSleepCalls returns the number of calls to a blocking sleep function
	// (Sleep and After).
	NumSleepCalls() int64

	// NumBlocking
	NumBlocking() int
}

// NewRealClock returns a Clock which simply delegates calls to the actual time
// package; it should be used by packages in production.
func NewRealClock() Clock {
	return &realClock{}
}

// NewFakeClock returns a FakeClock implementation which can be
// manually advanced through time for testing. The initial time of the
// FakeClock will be an arbitrary non-zero time.
func NewFakeClock() FakeClock {
	// use a fixture that does not fulfill Time.IsZero()
	return NewFakeClockAt(time.Date(1984, time.April, 4, 0, 0, 0, 0, time.UTC))
}

// NewFakeClockAt returns a FakeClock initialised at the given time.Time.
func NewFakeClockAt(t time.Time) FakeClock {
	return &fakeClock{
		time: t,
	}
}

type realClock struct{}

func (rc *realClock) After(d time.Duration) <-chan time.Time {
	return time.After(d)
}

func (rc *realClock) Sleep(d time.Duration) {
	time.Sleep(d)
}

func (rc *realClock) At(t time.Time) <-chan time.Time {
	return time.After(time.Until(t))
}

func (rc *realClock) Now() time.Time {
	return time.Now()
}

func (rc *realClock) Since(t time.Time) time.Duration {
	return rc.Now().Sub(t)
}

func (rc *realClock) NewTicker(d time.Duration) Ticker {
	return &realTicker{time.NewTicker(d)}
}

type fakeClock struct {
	sleepers []*sleeper
	blockers []*blocker
	time     time.Time
	updates  int64

	l sync.RWMutex
}

// sleeper represents a caller of After or Sleep
type sleeper struct {
	until time.Time
	done  chan time.Time
}

// blocker represents a caller of BlockUntil
type blocker struct {
	count int
	ch    chan struct{}
}

// NumSleepCalls returns the number of times that a blocking sleep function has
// been called (such as Sleep or After).
func (fc *fakeClock) NumSleepCalls() int64 {
	fc.l.RLock()
	n := fc.updates
	fc.l.RUnlock()
	return n
}

// After mimics time.After; it waits for the given duration to elapse on the
// fakeClock, then sends the current time on the returned channel.
func (fc *fakeClock) After(d time.Duration) <-chan time.Time {
	t := fc.Now().Add(d)
	return fc.At(t)
}

func (fc *fakeClock) At(t time.Time) <-chan time.Time {
	fc.l.Lock()

	fc.updates++
	done := make(chan time.Time, 1)

	if !t.After(fc.time) {
		// Trigger immediately.
		done <- fc.time
	} else {
		s := &sleeper{
			until: t,
			done:  done,
		}
		fc.sleepers = append(fc.sleepers, s)
		fc.blockers = notifyBlockers(fc.blockers, len(fc.sleepers))
	}

	fc.l.Unlock()
	return done
}

// notifyBlockers notifies all the blockers waiting until the
// given number of sleepers are waiting on the fakeClock. It
// returns an updated slice of blockers (i.e. those still waiting)
func notifyBlockers(blockers []*blocker, count int) (newBlockers []*blocker) {
	for _, b := range blockers {
		if b.count == count {
			close(b.ch)
		} else {
			newBlockers = append(newBlockers, b)
		}
	}
	return
}

// Sleep blocks until the given duration has passed on the fakeClock
func (fc *fakeClock) Sleep(d time.Duration) {
	<-fc.After(d)
}

// Time returns the current time of the fakeClock
func (fc *fakeClock) Now() time.Time {
	fc.l.RLock()
	t := fc.time
	fc.l.RUnlock()
	return t
}

// Since returns the duration that has passed since the given time on the fakeClock
func (fc *fakeClock) Since(t time.Time) time.Duration {
	return fc.Now().Sub(t)
}

func (fc *fakeClock) NewTicker(d time.Duration) Ticker {
	if d <= 0 {
		// Match behavior of time.NewTicker, so there are no surprises when
		// moving from fake clock to real clock.
		panic(errors.New("non-positive interval for NewTicker"))
	}

	ft := &fakeTicker{
		c:      make(chan time.Time, 1),
		stop:   make(chan bool, 1),
		clock:  fc,
		period: d,
	}
	ft.tick()
	return ft
}

func (fc *fakeClock) advanceTo(end time.Time) {
	fc.updates++
	var newSleepers []*sleeper
	for _, s := range fc.sleepers {
		if end.Sub(s.until) >= 0 {
			s.done <- end
		} else {
			newSleepers = append(newSleepers, s)
		}
	}
	fc.sleepers = newSleepers
	fc.blockers = notifyBlockers(fc.blockers, len(fc.sleepers))
	fc.time = end
}

// Advance advances fakeClock to a new point in time, ensuring channels from any
// previous invocations of After are notified appropriately before returning
func (fc *fakeClock) Advance(d time.Duration) {
	fc.l.Lock()
	defer fc.l.Unlock()
	end := fc.time.Add(d)
	fc.advanceTo(end)
}

func (fc *fakeClock) AdvanceTo(end time.Time) bool {
	fc.l.Lock()
	defer fc.l.Unlock()
	if fc.time.After(end) {
		return false
	}
	fc.advanceTo(end)
	return true
}

// BlockUntil will block until the fakeClock has the given number of sleepers
// (callers of Sleep or After)
func (fc *fakeClock) BlockUntil(n int) {
	fc.l.Lock()
	// Fast path: current number of sleepers is what we're looking for
	if len(fc.sleepers) == n {
		fc.l.Unlock()
		return
	}
	// Otherwise, set up a new blocker
	b := &blocker{
		count: n,
		ch:    make(chan struct{}),
	}
	fc.blockers = append(fc.blockers, b)
	fc.l.Unlock()
	<-b.ch
}

func (fc *fakeClock) NumBlocking() int {
	fc.l.RLock()
	defer fc.l.RUnlock()
	return len(fc.sleepers)
}

func (fc *fakeClock) NextWakeup() time.Time {
	fc.l.Lock()
	defer fc.l.Unlock()

	var end time.Time
	for i, s := range fc.sleepers {
		if i == 0 || s.until.Before(end) {
			end = s.until
		}
	}

	return end
}
