package main

import (
	"context"
	"errors"
	"fmt"
)

// defaultCheckpointEvery sets how often Loop.Run snapshots state mid-task.
// Upstream picks 5 (`guide/error-handling.md` L307-L312, `if turn % 5 == 0`).
// Higher = less disk I/O but more rework on crash; lower = more I/O but
// finer-grained resume. 5 is a sensible default for an LLM-bound loop where
// each turn already costs hundreds of ms.
const defaultCheckpointEvery = 5

// defaultMaxTurns mirrors upstream's 50-turn cap at L289. The loop must have
// SOME upper bound or a confused model can loop forever. The CLI demo and
// tests override this when they need a tighter or looser cap.
const defaultMaxTurns = 50

// ErrMaxTurnsReached signals the loop hit its turn cap without the provider
// reporting done=true. Distinct from a provider error so callers can decide
// to surface vs retry.
var ErrMaxTurnsReached = errors.New("loop: max turns reached")

// Loop is the toy agentic loop for this chapter. It is intentionally simpler
// than s01/s02's loops: no tool registry, no streaming, no parallel tool
// calls. The whole purpose is to demonstrate how Save/Load/Clear compose with
// a loop's iteration variable. The Provider produces an assistant message
// per turn; when the provider reports done, the loop clears the checkpoint
// and returns the final history.
//
// Concurrency: a Loop value is single-use. Don't share it between goroutines.
type Loop struct {
	// Store is where checkpoints land. Nil means "don't persist" — useful in
	// tests that only want to exercise the iteration logic without disk I/O.
	Store *CheckpointStore

	// Provider is the source of assistant messages. See types.go.
	Provider Provider

	// MaxTurns caps the loop. Defaults to defaultMaxTurns. A value <= 0 is
	// replaced with the default at Run time so callers can leave it zero.
	MaxTurns int

	// CheckpointEvery controls the cadence of mid-task saves. Defaults to
	// defaultCheckpointEvery. A value <= 0 is replaced.
	CheckpointEvery int

	// PanicAtTurn, if >= 0, mirrors MockProvider.PanicAtTurn but for crashes
	// triggered by the loop itself AFTER the provider returns successfully.
	// This lets tests force a panic AT a specific turn while still letting
	// the provider's per-turn save complete. -1 (the default) means "never
	// panic from the loop".
	//
	// We expose it on Loop (not just on the provider) because the canonical
	// test "resume after crash" wants the panic to happen just AFTER the
	// checkpoint write, simulating a crash in the OS / a kill -9 between
	// turns. The provider's PanicAtTurn would crash BEFORE that turn's write.
	PanicAtTurn int
}

// LoadOrStartFresh checks whether a checkpoint for taskID exists. If yes, it
// returns the checkpoint's Messages and Turn — that's our resume hand-off.
// If no, it returns a fresh single-message history seeded with userMessage
// and Turn=0.
//
// We return startedFresh as a third value so the CLI demo / tests can print
// "Resumed from turn N" vs "Started fresh" without checking len(messages).
func (l *Loop) LoadOrStartFresh(taskID, userMessage string) (history []Message, turn int, startedFresh bool, err error) {
	if l.Store != nil {
		cp, lerr := l.Store.Load(taskID)
		if lerr != nil {
			return nil, 0, false, fmt.Errorf("loop: load checkpoint %s: %w", taskID, lerr)
		}
		if cp != nil {
			// Defensive: clone messages so a caller mutating the returned
			// slice can't accidentally mutate the on-disk checkpoint's
			// in-memory mirror (we re-Load the file each Run, so this is
			// belt-and-suspenders but cheap).
			cloned := make([]Message, len(cp.Messages))
			copy(cloned, cp.Messages)
			return cloned, cp.Turn, false, nil
		}
	}
	// Fresh start. Seed the conversation with the user's prompt.
	return []Message{{Role: "user", Content: userMessage}}, 0, true, nil
}

// Run drives the loop until the provider returns done=true OR an error
// surfaces OR MaxTurns is hit. The taskID is the checkpoint key; the same
// taskID across runs is how a process restart finds its previous state.
//
// The user-visible sequence on a graceful run:
//
//  1. LoadOrStartFresh seeds history.
//  2. For each turn: Provider.Next → append assistant message → maybe save.
//  3. When provider says done: Store.Clear(taskID); return.
//
// On a crash: nothing here can prevent a panic propagating up; that's the
// point. The most recent checkpoint on disk is the resume point. The next
// invocation with the same taskID picks up where this one left off.
func (l *Loop) Run(ctx context.Context, taskID, userMessage string) ([]Message, error) {
	maxTurns := l.MaxTurns
	if maxTurns <= 0 {
		maxTurns = defaultMaxTurns
	}
	checkpointEvery := l.CheckpointEvery
	if checkpointEvery <= 0 {
		checkpointEvery = defaultCheckpointEvery
	}

	history, turn, _, err := l.LoadOrStartFresh(taskID, userMessage)
	if err != nil {
		return nil, err
	}

	// Iterate from the loaded turn (0 on fresh start, N on resume) up to
	// maxTurns. The provider's Next is the work; everything else is bookkeeping.
	for ; turn < maxTurns; turn++ {
		// Context cancellation check before each turn. We don't cancel
		// mid-Next — the provider doesn't take ctx-awareness as a contract
		// in this toy — but we honor it between turns.
		if err := ctx.Err(); err != nil {
			return history, err
		}

		msg, done, err := l.Provider.Next(ctx, turn, history)
		if err != nil {
			// On error we save the current state before propagating so the
			// retry has a checkpoint to resume from. Matches upstream's
			// `except RetryExhausted` arm at L314-L320.
			if l.Store != nil {
				_ = l.Store.Save(&Checkpoint{
					TaskID:   taskID,
					Turn:     turn,
					Messages: history,
				})
			}
			return history, fmt.Errorf("loop: provider error at turn %d: %w", turn, err)
		}
		history = append(history, msg)

		// Loop-level panic seam: AFTER appending the message but BEFORE the
		// next checkpoint cadence boundary. Tests use this to simulate a
		// crash that happens AFTER the previous checkpoint but BEFORE this
		// turn's save. The resumed loop should start at the LAST CHECKPOINTED
		// turn, not at this one.
		if l.PanicAtTurn >= 0 && turn == l.PanicAtTurn {
			panic(fmt.Sprintf("loop: induced panic at turn %d", turn))
		}

		if done {
			// Graceful exit. Clear the checkpoint so a future run with this
			// taskID starts fresh — matches upstream's L296.
			if l.Store != nil {
				if err := l.Store.Clear(taskID); err != nil {
					return history, fmt.Errorf("loop: clear checkpoint: %w", err)
				}
			}
			return history, nil
		}

		// Checkpoint cadence: save every N turns. We save AFTER appending the
		// turn's output so the saved state reflects the LATEST completed
		// turn. We save the NEXT turn number so resume starts past the
		// already-completed one.
		if l.Store != nil && (turn+1)%checkpointEvery == 0 {
			if err := l.Store.Save(&Checkpoint{
				TaskID:   taskID,
				Turn:     turn + 1,
				Messages: history,
			}); err != nil {
				return history, fmt.Errorf("loop: save checkpoint at turn %d: %w", turn, err)
			}
		}
	}

	// Fell out of the loop without seeing done=true. Save final state and
	// surface the max-turns error so the caller can decide to retry with a
	// larger budget.
	if l.Store != nil {
		_ = l.Store.Save(&Checkpoint{
			TaskID:   taskID,
			Turn:     turn,
			Messages: history,
		})
	}
	return history, ErrMaxTurnsReached
}
