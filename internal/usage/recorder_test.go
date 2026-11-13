package usage

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/kinako404/conloom/internal/obs"
	"github.com/kinako404/conloom/internal/store"
)

// fakeSink records every batch it is given. While block is open, writes wait
// on it; fail makes writes return an error.
type fakeSink struct {
	mu      sync.Mutex
	batches [][]store.ModelCall
	block   chan struct{}
	fail    bool
	wrote   chan int
}

func newFakeSink() *fakeSink { return &fakeSink{wrote: make(chan int, 100)} }

func (s *fakeSink) InsertModelCalls(ctx context.Context, calls []store.ModelCall) error {
	if s.block != nil {
		select {
		case <-s.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.mu.Lock()
	fail := s.fail
	if !fail {
		s.batches = append(s.batches, append([]store.ModelCall(nil), calls...))
	}
	s.mu.Unlock()
	s.wrote <- len(calls)
	if fail {
		return errors.New("test: disk full")
	}
	return nil
}

func (s *fakeSink) all() []store.ModelCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []store.ModelCall
	for _, b := range s.batches {
		out = append(out, b...)
	}
	return out
}

func call(i int) store.ModelCall {
	return store.ModelCall{At: time.Now(), BotInstanceID: "qq-main", Purpose: store.PurposeChat, Model: "m", TaskID: fmt.Sprint(i)}
}

func waitWrite(t *testing.T, s *fakeSink, within time.Duration) int {
	t.Helper()
	select {
	case n := <-s.wrote:
		return n
	case <-time.After(within):
		t.Fatalf("no write within %v", within)
		return 0
	}
}

type metrics struct {
	*obs.Metrics
	reg *prometheus.Registry
}

func newMetrics() metrics {
	reg := prometheus.NewRegistry()
	return metrics{obs.NewMetrics(reg), reg}
}

// counter returns the value of the unlabelled counter name, 0 when absent.
func (m metrics) counter(t *testing.T, name string) float64 {
	t.Helper()
	mfs, err := m.reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() == name && len(mf.GetMetric()) == 1 {
			return mf.GetMetric()[0].GetCounter().GetValue()
		}
	}
	return 0
}

func TestRecorder_FlushesOnBatchAndInterval(t *testing.T) {
	s := newFakeSink()
	r := NewRecorder(s, RecorderOptions{Batch: 3, Flush: 50 * time.Millisecond, Metrics: newMetrics().Metrics})
	t.Cleanup(func() { _ = r.Close(context.Background()) })

	for i := range 3 {
		r.Record(call(i))
	}
	if n := waitWrite(t, s, 45*time.Millisecond); n != 3 {
		t.Fatalf("first write = %d calls, want a full batch of 3", n)
	}
	start := time.Now()
	r.Record(call(3))
	if n := waitWrite(t, s, time.Second); n != 1 {
		t.Fatalf("second write = %d calls, want 1", n)
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Errorf("partial batch written after %v, want about 50ms", d)
	}
	if got := s.all(); len(got) != 4 || got[3].TaskID != "3" {
		t.Errorf("written = %+v", got)
	}
}

func TestRecorder_DropsWhenFull(t *testing.T) {
	s := newFakeSink()
	s.block = make(chan struct{})
	m := newMetrics()
	r := NewRecorder(s, RecorderOptions{Buffer: 2, Batch: 1, Flush: time.Hour, Metrics: m.Metrics})

	r.Record(call(0))
	waitTaken(t, r)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 1; i <= 5; i++ {
			r.Record(call(i))
		}
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Record blocked on a full buffer")
	}
	if got := m.counter(t, "conloom_usage_dropped_total"); got != 3 {
		t.Errorf("UsageDropped = %v, want 3", got)
	}
	close(s.block)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := r.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := len(s.all()); got != 3 {
		t.Errorf("written = %d calls, want the 3 that fit", got)
	}
}

// waitTaken waits until the writer has taken every queued call.
func waitTaken(t *testing.T, r *Recorder) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for len(r.ch) > 0 {
		if time.Now().After(deadline) {
			t.Fatal("writer did not take the queued call")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestRecorder_CloseFlushes(t *testing.T) {
	s := newFakeSink()
	r := NewRecorder(s, RecorderOptions{Batch: 200, Flush: time.Hour})
	for i := range 450 {
		r.Record(call(i))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := r.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := len(s.all()); got != 450 {
		t.Fatalf("written after Close = %d, want 450", got)
	}
	r.Record(call(999))
	if err := r.Close(ctx); err != nil {
		t.Errorf("second Close: %v", err)
	}
	if got := len(s.all()); got != 450 {
		t.Errorf("written after a Record past Close = %d, want 450", got)
	}

	var nilRec *Recorder
	nilRec.Record(call(0))
	if err := nilRec.Close(ctx); err != nil {
		t.Errorf("nil Close: %v", err)
	}
}

func TestRecorder_CloseStopsAtDeadline(t *testing.T) {
	s := newFakeSink()
	s.block = make(chan struct{})
	t.Cleanup(func() { close(s.block) })
	r := NewRecorder(s, RecorderOptions{Batch: 1, Flush: time.Hour})
	r.Record(call(0))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := r.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Close = %v, want the deadline", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("Close took %v", d)
	}
}

func TestRecorder_WriteErrorCounted(t *testing.T) {
	s := newFakeSink()
	s.fail = true
	m := newMetrics()
	r := NewRecorder(s, RecorderOptions{Batch: 2, Flush: time.Hour, Metrics: m.Metrics})
	t.Cleanup(func() { _ = r.Close(context.Background()) })
	r.Record(call(0))
	r.Record(call(1))
	waitWrite(t, s, time.Second)
	deadline := time.Now().Add(time.Second)
	for m.counter(t, "conloom_usage_write_errors_total") != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("UsageWriteErrors = %v, want 1", m.counter(t, "conloom_usage_write_errors_total"))
		}
		time.Sleep(time.Millisecond)
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

var _ net.Error = timeoutErr{}

func TestOutcome(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{nil, store.OutcomeOK},
		{fmt.Errorf("model: %w", context.Canceled), store.OutcomeCanceled},
		{fmt.Errorf("model: %w", context.DeadlineExceeded), store.OutcomeTimeout},
		{&net.OpError{Op: "read", Err: timeoutErr{}}, store.OutcomeTimeout},
		{errors.New("upstream 500"), store.OutcomeError},
	} {
		if got := Outcome(c.err); got != c.want {
			t.Errorf("Outcome(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}

func TestAttribution_RoundTrip(t *testing.T) {
	if got := AttributionFrom(context.Background()); got != (Attribution{}) {
		t.Errorf("AttributionFrom(empty) = %+v", got)
	}
	a := Attribution{BotInstanceID: "qq-main", ConversationKey: "qq-main:qq:group:10001", TaskID: "task_1"}
	if got := AttributionFrom(WithAttribution(context.Background(), a)); got != a {
		t.Errorf("AttributionFrom = %+v, want %+v", got, a)
	}
}
