// Package sink defines the interface for receiving extracted APK artifacts.
// Implement Sink to push artifacts to MDM systems, cloud storage, webhooks,
// or any other downstream consumer. Wire your implementation into the watcher
// at startup — the filesystem write always happens first, then your sink fires.
package sink

import "context"

// Artifact describes a successfully extracted APK or XAPK.
type Artifact struct {
	Package     string
	VersionCode int
	VersionName string
	Path        string // absolute path to the written file
	Checksum    string // SHA256 hex
	IsXAPK      bool
}

// Sink receives a completed artifact after each successful extraction.
type Sink interface {
	OnArtifact(ctx context.Context, a Artifact) error
}

// Noop is the default sink — does nothing. Use it when you only need the
// filesystem output and have no downstream system to notify.
type Noop struct{}

func (Noop) OnArtifact(_ context.Context, _ Artifact) error { return nil }

// Fanout calls each sink in order. The first error aborts the chain and is
// returned; subsequent sinks are not called.
type Fanout struct {
	sinks []Sink
}

func NewFanout(sinks ...Sink) *Fanout {
	return &Fanout{sinks: sinks}
}

func (f *Fanout) OnArtifact(ctx context.Context, a Artifact) error {
	for _, s := range f.sinks {
		if err := s.OnArtifact(ctx, a); err != nil {
			return err
		}
	}
	return nil
}
