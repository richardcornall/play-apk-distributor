package sink_test

import (
	"context"
	"errors"
	"testing"

	"github.com/richardcornall/apk-distributor/sink"
)

var testArtifact = sink.Artifact{
	Package:     "com.example.app",
	VersionCode: 42,
	VersionName: "4.2",
	Path:        "/output/com.example.app/42.xapk",
	Checksum:    "abc123",
	IsXAPK:      true,
}

// --- Noop ---

func TestNoop_AlwaysSucceeds(t *testing.T) {
	var s sink.Noop
	if err := s.OnArtifact(context.Background(), testArtifact); err != nil {
		t.Errorf("Noop.OnArtifact returned error: %v", err)
	}
}

func TestNoop_ImplementsInterface(t *testing.T) {
	var _ sink.Sink = sink.Noop{}
}

// --- Fanout ---

func TestFanout_CallsAllSinks(t *testing.T) {
	calls := 0
	recorder := &recorderSink{fn: func(_ sink.Artifact) { calls++ }}
	f := sink.NewFanout(recorder, recorder, recorder)
	if err := f.OnArtifact(context.Background(), testArtifact); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 3 {
		t.Errorf("expected 3 calls, got %d", calls)
	}
}

func TestFanout_AbortsOnFirstError(t *testing.T) {
	sentinel := errors.New("sink failed")
	calls := 0
	failing := &errorSink{err: sentinel}
	counter := &recorderSink{fn: func(_ sink.Artifact) { calls++ }}

	f := sink.NewFanout(counter, failing, counter)
	err := f.OnArtifact(context.Background(), testArtifact)
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel error, got: %v", err)
	}
	// first counter fired, then failing aborted — second counter never runs
	if calls != 1 {
		t.Errorf("expected 1 call before abort, got %d", calls)
	}
}

func TestFanout_EmptySucceeds(t *testing.T) {
	f := sink.NewFanout()
	if err := f.OnArtifact(context.Background(), testArtifact); err != nil {
		t.Errorf("empty fanout should not error: %v", err)
	}
}

func TestFanout_PassesArtifactUnchanged(t *testing.T) {
	var got sink.Artifact
	capture := &recorderSink{fn: func(a sink.Artifact) { got = a }}
	f := sink.NewFanout(capture)
	_ = f.OnArtifact(context.Background(), testArtifact)
	if got != testArtifact {
		t.Errorf("artifact mutated in fanout: %+v", got)
	}
}

// --- helpers ---

type recorderSink struct {
	fn func(sink.Artifact)
}

func (r *recorderSink) OnArtifact(_ context.Context, a sink.Artifact) error {
	r.fn(a)
	return nil
}

type errorSink struct {
	err error
}

func (e *errorSink) OnArtifact(_ context.Context, _ sink.Artifact) error {
	return e.err
}
