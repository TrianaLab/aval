package main

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"time"
)

// spinDelay hides the spinner on fast registry calls: it shows only after this.
const spinDelay = 300 * time.Millisecond

// eraseLine moves to column 0 and clears the line.
const eraseLine = "\r\x1b[K"

var spinFrames = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

// Seams for the spinner tests: a fake clock and fake signals.
var (
	spinTicker = func() (<-chan time.Time, func()) {
		t := time.NewTicker(100 * time.Millisecond)
		return t.C, t.Stop
	}
	interrupts = func() (<-chan os.Signal, func()) {
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt)
		return c, func() { signal.Stop(c) }
	}
	// raise sends s to this process again, now with the default handler.
	raise = func(s os.Signal) error {
		p, _ := os.FindProcess(os.Getpid()) // ponytail: never fails on Unix; Signal fails on Windows
		return p.Signal(s)
	}
	exit = os.Exit
)

// spinner shows `⠹ <label>… 4s` on a terminal while Aval waits on the
// registry. Writes through it erase the line first, so a warning printed
// during the wait never lands in the middle of it.
type spinner struct {
	mu       sync.Mutex
	w        io.Writer
	shown    bool
	done     chan struct{}
	finished chan struct{}
}

// spin starts a spinner for label on w when on (styled output, stderr a
// terminal) and returns the writer to use for w meanwhile and the function
// that stops it. stop erases the line and returns only once nothing more
// can be written, so it runs before anything else prints (the runner).
// Without on it returns w and a no-op.
func spin(w io.Writer, on bool, label string) (io.Writer, func()) {
	if !on {
		return w, func() {}
	}
	s := &spinner{w: w, done: make(chan struct{}), finished: make(chan struct{})}
	ticks, stopTicks := spinTicker()
	sigs, stopSigs := interrupts()
	start := now()
	go func() {
		defer close(s.finished)
		defer stopTicks()
		defer stopSigs()
		for i := 0; ; i++ {
			select {
			case <-s.done:
				s.clear()
				return
			case sig := <-sigs:
				// Ctrl-C: leave the terminal clean, then die of the signal as
				// before; exit 130 where it cannot be re-raised (Windows).
				s.clear()
				stopSigs()
				if raise(sig) != nil {
					exit(130)
				}
				return
			case t := <-ticks:
				if d := t.Sub(start); d >= spinDelay {
					s.mu.Lock()
					fmt.Fprintf(s.w, "%s%c %s… %ds", eraseLine, spinFrames[i%len(spinFrames)], label, int(d.Seconds()))
					s.shown = true
					s.mu.Unlock()
				}
			}
		}
	}()
	var once sync.Once
	return s, func() {
		once.Do(func() { close(s.done) })
		<-s.finished
	}
}

// Write erases the spinner line, then writes p.
func (s *spinner) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.erase()
	return s.w.Write(p)
}

func (s *spinner) clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.erase()
}

// erase clears the line if the spinner is on it; s.mu is held.
func (s *spinner) erase() {
	if s.shown {
		fmt.Fprint(s.w, eraseLine)
		s.shown = false
	}
}
