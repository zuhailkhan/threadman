package handoff

import "context"

// Stage is what a summary is doing.
type Stage int

const (
	// StageWriting is the request that writes a brief: from the whole
	// transcript, or from notes on its parts.
	StageWriting Stage = iota
	// StageReading is taking notes on one part of a transcript too long for
	// one request.
	StageReading
	// StageCondensing is merging notes that are too long to send together.
	StageCondensing
)

// Progress is how far a handoff or merge has got, for showing while it runs.
type Progress struct {
	Stage Stage
	// Part and Parts count the parts being read or the groups of notes being
	// condensed; both are 0 for a request covering everything at once.
	Part, Parts int
	// Thread and Threads are set for a merge: which thread is being
	// summarized and how many there are. Done is how many are finished.
	Thread, Threads, Done int
}

type progressKey struct{}

// WithProgress returns a context whose summaries report their progress to fn.
// fn is called from the goroutine doing the work, so it must not block.
func WithProgress(ctx context.Context, fn func(Progress)) context.Context {
	return context.WithValue(ctx, progressKey{}, fn)
}

func progressFunc(ctx context.Context) func(Progress) {
	fn, _ := ctx.Value(progressKey{}).(func(Progress))
	return fn
}

func report(ctx context.Context, p Progress) {
	if fn := progressFunc(ctx); fn != nil {
		fn(p)
	}
}
