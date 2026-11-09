package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/TrianaLab/aval/internal/registry"
	"github.com/TrianaLab/aval/pkg/evidence"
)

// syncBuffer is a fake terminal the spinner goroutine and the test share.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

var t0 = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

// fakeSpinClock freezes now at t0 and returns the tick and signal channels
// the spinner reads, unbuffered so a send returns once the spinner took it.
func fakeSpinClock(t *testing.T) (ticks chan time.Time, sigs chan os.Signal) {
	t.Helper()
	n, tk, in := now, spinTicker, interrupts
	t.Cleanup(func() { now, spinTicker, interrupts = n, tk, in })
	ticks, sigs = make(chan time.Time), make(chan os.Signal)
	now = func() time.Time { return t0 }
	spinTicker = func() (<-chan time.Time, func()) { return ticks, func() {} }
	interrupts = func() (<-chan os.Signal, func()) { return sigs, func() {} }
	return ticks, sigs
}

// The spinner never starts in plain mode or before 300ms, is always erased,
// and a write through it erases the line first.
func TestSpinner(t *testing.T) {
	ticks, _ := fakeSpinClock(t)
	var buf syncBuffer

	w, stop := spin(&buf, false, "looking up r")
	stop()
	if w != &buf || buf.String() != "" {
		t.Fatalf("plain mode: writer %v, wrote %q", w, buf.String())
	}

	_, stop = spin(&buf, true, "looking up r")
	ticks <- t0.Add(100 * time.Millisecond)
	ticks <- t0.Add(299 * time.Millisecond)
	stop()
	stop() // idempotent
	if buf.String() != "" {
		t.Fatalf("before 300ms: wrote %q", buf.String())
	}

	w, stop = spin(&buf, true, "looking up r")
	ticks <- t0.Add(300 * time.Millisecond)
	ticks <- t0.Add(4200 * time.Millisecond)
	ticks <- t0 // draws nothing; taken only once the 4s frame is written
	if _, err := w.Write([]byte("aval: warning: x\n")); err != nil {
		t.Fatal(err)
	}
	stop()
	want := eraseLine + "⠋ looking up r… 0s" + eraseLine + "⠙ looking up r… 4s" + eraseLine + "aval: warning: x\n"
	if buf.String() != want {
		t.Fatalf("got  %q\nwant %q", buf.String(), want)
	}
}

// Ctrl-C during a wait erases the line, then dies of the signal again, or
// exits 130 where the signal cannot be re-raised (Windows).
func TestSpinnerInterrupt(t *testing.T) {
	r, e := raise, exit
	t.Cleanup(func() { raise, exit = r, e })
	for _, raiseErr := range []error{nil, errors.New("not supported")} {
		ticks, sigs := fakeSpinClock(t)
		var buf syncBuffer
		var raised os.Signal
		code := -1
		raise = func(s os.Signal) error { raised = s; return raiseErr }
		exit = func(c int) { code = c }
		_, stop := spin(&buf, true, "looking up r")
		ticks <- t0.Add(time.Second)
		sigs <- os.Interrupt
		stop() // returns: the spinner is gone
		wantCode := -1
		if raiseErr != nil {
			wantCode = 130
		}
		if !strings.HasSuffix(buf.String(), "1s"+eraseLine) || raised != os.Interrupt || code != wantCode {
			t.Fatalf("raise error %v: wrote %q, raised %v, exit %d", raiseErr, buf.String(), raised, code)
		}
	}
}

// The real seams: the ticker ticks, raise reaches this process.
func TestSpinnerSeams(t *testing.T) {
	c, stopTicks := spinTicker()
	<-c
	stopTicks()
	_, stopSigs := interrupts()
	stopSigs()
	_ = raise(syscall.Signal(0)) // signal 0 only checks the process exists
}

// aval run on a terminal: the spinner shows while the registry is asked,
// is erased before the decision, and the runner writes only after it.
func TestRunSpinnerStopsBeforeRunner(t *testing.T) {
	onTTY(t, "1")
	ticks, _ := fakeSpinClock(t)
	fakeVerify(t, evidence.ErrNotFound, nil, nil)
	lk := lookup
	lookup = func(ctx context.Context, repo, digest string) ([]registry.Descriptor, error) {
		ticks <- t0.Add(2 * time.Second)
		return lk(ctx, repo, digest)
	}
	c := contractWith(t, "hermetic", "sh", "-c", "echo RUNNER")
	var term syncBuffer
	run([]string{"run", "-f", c, "--registry", "reg.example/r", "--policy", "p.yaml", "--result-file", t.TempDir() + "/r.json"}, &term, &term)
	out := term.String()
	frame := strings.Index(out, "looking up reg.example/r… 2s")
	erase := strings.LastIndex(out, eraseLine)
	runner := strings.Index(out, "RUNNER")
	if frame < 0 || erase < frame || runner < erase || strings.Count(out, eraseLine) != 2 {
		t.Fatalf("spinner not erased before the runner:\n%q", out)
	}
}
