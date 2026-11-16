// Package conversation treats a group of linked sessions, one or more per
// tool, as a single conversation: one timeline in time order, the tool that
// was active last, and what a given tool has not seen yet.
package conversation

import (
	"slices"
	"time"

	"github.com/zuhailkhan/threadman/internal/domain"
)

// Entry is one message on a conversation's timeline, with the session it
// belongs to.
type Entry struct {
	Provider string
	ThreadID string
	Message  domain.Message
}

// Timeline merges the messages of a group's sessions into time order. Entries
// with the same timestamp keep their order within a session, and sessions
// keep the order they were given in.
func Timeline(threads []domain.Thread) []Entry {
	var entries []Entry
	for _, t := range threads {
		for _, m := range t.Messages {
			entries = append(entries, Entry{Provider: t.Provider, ThreadID: t.ID, Message: m})
		}
	}
	slices.SortStableFunc(entries, func(a, b Entry) int {
		return a.Message.Timestamp.Compare(b.Message.Timestamp)
	})
	return entries
}

// lastReceived is the time of the newest handoff block a provider's sessions
// received. Each block carried everything the provider had missed until then,
// so it marks how far the provider has seen into the other tools' messages.
func lastReceived(entries []Entry, provider string) (at time.Time, ok bool) {
	for _, e := range entries {
		if e.Provider == provider && e.Message.Kind == domain.KindHandoff && !e.Message.Timestamp.Before(at) {
			at, ok = e.Message.Timestamp, true
		}
	}
	return at, ok
}

// Missed returns what a provider has not seen: the messages written in other
// tools since the last handoff it received, or all of them if it never
// received one. Its own messages don't count, so a tool that kept going after
// handing off still catches up on everything the other tool did meanwhile.
// Handoff blocks are left out; they repeat messages that are on the timeline
// in their own right.
func Missed(entries []Entry, provider string) []Entry {
	since, received := lastReceived(entries, provider)
	var out []Entry
	for _, e := range entries {
		if e.Message.Kind == domain.KindHandoff || e.Provider == provider {
			continue
		}
		if received && !e.Message.Timestamp.After(since) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// Everything returns every message on the timeline, leaving out handoff
// blocks: what a tool starting a new session needs, including its own turns
// from a session it has since deleted.
func Everything(entries []Entry) []Entry {
	var out []Entry
	for _, e := range entries {
		if e.Message.Kind != domain.KindHandoff {
			out = append(out, e)
		}
	}
	return out
}

// Session returns the session a provider's turns continue in: the one with
// its newest entry. A session the tool no longer has can't be continued, so
// it is skipped. ok is false when the provider has no session to continue.
func Session(threads []domain.Thread, provider string) (domain.Thread, bool) {
	var (
		best   domain.Thread
		bestAt time.Time
		found  bool
	)
	for _, t := range threads {
		if t.Provider != provider || t.SourceMissing {
			continue
		}
		at := newest(t)
		if !found || at.After(bestAt) {
			best, bestAt, found = t, at, true
		}
	}
	return best, found
}

// Head returns the session with the newest message: where the conversation
// was last active.
func Head(threads []domain.Thread) (domain.Thread, bool) {
	var (
		best   domain.Thread
		bestAt time.Time
	)
	for i, t := range threads {
		if at := newest(t); i == 0 || at.After(bestAt) {
			best, bestAt = t, at
		}
	}
	return best, len(threads) > 0
}

func newest(t domain.Thread) time.Time {
	var at time.Time
	for _, m := range t.Messages {
		if m.Timestamp.After(at) {
			at = m.Timestamp
		}
	}
	return at
}

// Messages turns entries back into messages, tagging each with the provider
// it was written in, so a handoff can label every turn with its source.
func Messages(entries []Entry) []domain.Message {
	out := make([]domain.Message, len(entries))
	for i, e := range entries {
		out[i] = e.Message
		out[i].Provider = e.Provider
	}
	return out
}
