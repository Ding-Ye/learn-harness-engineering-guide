package main

import (
	"context"
	"errors"
	"fmt"
)

// Local copies of the message/provider shapes. The curriculum's isolation
// rule: s11 ships its own types so a reader on this chapter doesn't have to
// page back to s02. The shapes are deliberately tiny — checkpoint/resume is
// about persisting state across crashes, not about producing realistic LLM
// transcripts. A single text field per Message is enough to demonstrate the
// round-trip invariant.

// Message is one entry in the conversation history. We use a flat shape (no
// content blocks) because the checkpoint test only needs to round-trip the
// structure through JSON; richer types just inflate the test surface without
// teaching anything new.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Provider abstracts whatever produces the next assistant message. The toy
// loop in this chapter doesn't need tool calls — it just walks a fixed
// progression of (turn, transcript) until the provider returns Done=true.
// Real harnesses wrap an LLM client here (see s02); we use MockProvider
// because the chapter's lesson is about checkpoint atomicity, not generation.
type Provider interface {
	// Next produces the assistant message for the given turn given the running
	// history. Returning Done=true means "the task is finished, the loop
	// should clear the checkpoint and exit." Returning err means the loop
	// should propagate without clearing.
	Next(ctx context.Context, turn int, history []Message) (msg Message, done bool, err error)
}

// MockProvider is a deterministic test/demo provider. It emits a scripted
// sequence of assistant messages and reports Done=true on the very last one.
// The optional `panicAtTurn` field lets tests force a panic mid-task — used
// to verify the loop's checkpoint was already on disk when the crash hit.
type MockProvider struct {
	// Script is the canned set of assistant messages. The loop walks the
	// script by turn index; once we run off the end, Next returns done=true
	// to terminate the loop gracefully.
	Script []string

	// PanicAtTurn, if >= 0, makes Next panic when called with `turn ==
	// PanicAtTurn`. Used by TestLoop_ResumesFromCheckpoint to simulate a
	// mid-task crash AFTER a checkpoint was written. -1 means "never panic".
	PanicAtTurn int

	// FailAtTurn, if >= 0, makes Next return a regular error (no panic) at
	// the given turn. Used by tests that want graceful error handling rather
	// than process death.
	FailAtTurn int

	// Calls counts how many times Next was invoked, across all runs of this
	// provider instance. Tests assert on this to confirm resume actually
	// skipped the already-completed turns.
	Calls int
}

// errMockFail is the marker error MockProvider.Next returns when FailAtTurn
// fires. Tests use errors.Is to check whether the loop surfaced it.
var errMockFail = errors.New("mock provider: induced failure")

// Next implements the Provider interface. Two side channels:
//   - PanicAtTurn: makes the call panic, which tests recover() from inside
//     the loop's goroutine wrapper. Simulates a hard crash.
//   - FailAtTurn: returns a normal error. Simulates a recoverable failure
//     that the caller can decide to retry.
//
// On the normal path, Next returns the scripted message for `turn`. If we've
// run off the end of Script, we report done=true so the loop terminates.
func (m *MockProvider) Next(_ context.Context, turn int, _ []Message) (Message, bool, error) {
	m.Calls++

	// Panic path — used to simulate a crash that bypasses the loop's normal
	// error handling.
	if m.PanicAtTurn >= 0 && turn == m.PanicAtTurn {
		panic(fmt.Sprintf("mock provider: induced panic at turn %d", turn))
	}

	// Error path — graceful failure. The loop's caller can decide to retry.
	if m.FailAtTurn >= 0 && turn == m.FailAtTurn {
		return Message{}, false, errMockFail
	}

	// End-of-script: signal done.
	if turn >= len(m.Script) {
		return Message{
			Role:    "assistant",
			Content: "task complete",
		}, true, nil
	}

	return Message{
		Role:    "assistant",
		Content: m.Script[turn],
	}, false, nil
}
