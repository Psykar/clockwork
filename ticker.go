package clockwork

import (
	"time"
)

// Ticker provides an interface which can be used instead of directly
// using the ticker within the time module. The real-time ticker t
// provides ticks through t.C which becomes now t.Chan() to make
// this channel requirement definable in this interface.
type Ticker interface {
	Chan() <-chan time.Time
	Stop()
}

type realTicker struct{ *time.Ticker }

func (rt *realTicker) Chan() <-chan time.Time {
	return rt.C
}

type fakeTicker struct {
	c      chan time.Time
	stop   chan bool
	clock  FakeClock
	period time.Duration
}

func (ft *fakeTicker) Chan() <-chan time.Time {
	return ft.c
}

func (ft *fakeTicker) Stop() {
	ft.stop <- true
}

// tick sends the tick time to the ticker channel after every period.
// Tick events are discarded if the underlying ticker channel does
// not have enough capacity.
func (ft *fakeTicker) tick() {
	tick := ft.clock.Now()
	go func() {
		for {
			tick = tick.Add(ft.period)

			select {
			case <-ft.stop:
				return
			case t := <-ft.clock.At(tick):
				// Non-blocking send. Drop ticks rather than blocking.
				select {
				// time.Ticker docs say this returns the current time.
				case ft.c <- t:
				default:
				}
			}
		}
	}()
}
