package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// main demonstrates the checkpoint/resume cycle end-to-end:
//
//  1. Fresh run: feed an 8-turn provider into a loop. Force a panic AT turn 6
//     after the every-5-turns checkpoint at turn 5 has been written. The
//     process crashes; the goroutine wrapper around Run prints the panic but
//     doesn't take the demo down.
//  2. Resume run: same TaskID, fresh Loop. LoadOrStartFresh sees the
//     checkpoint and restarts at turn 5. The provider runs turns 5-7, hits
//     done, clears the checkpoint, exits.
//
// The output makes the resume property visible: the second Provider's `Calls`
// is 3 (turns 5, 6, 7), not 8 — turns 0-4 were skipped because their
// completion was already on disk.
func main() {
	dir, err := os.MkdirTemp("", "s11-checkpoint-demo-*")
	if err != nil {
		fmt.Println("mkdir:", err)
		os.Exit(1)
	}
	defer os.RemoveAll(dir)
	fmt.Printf("=== s11 checkpoint/resume demo ===\ncheckpoint dir: %s\n\n", dir)

	store, err := NewCheckpointStore(dir)
	if err != nil {
		fmt.Println("store init:", err)
		os.Exit(1)
	}

	const taskID = "demo-task"
	script := []string{
		"step 0: understand the request",
		"step 1: read input file",
		"step 2: plan the changes",
		"step 3: write candidate solution",
		"step 4: run tests",
		"step 5: fix failing test",
		"step 6: re-run tests",
		"step 7: write summary",
	}

	// --- Run 1: crashes at turn 6. ---
	fmt.Println("--- Run 1: crashes after turn 6 ---")
	provider1 := &MockProvider{
		Script:      script,
		PanicAtTurn: -1,
		FailAtTurn:  -1,
	}
	loop1 := &Loop{
		Store:           store,
		Provider:        provider1,
		MaxTurns:        20,
		CheckpointEvery: 5,
		PanicAtTurn:     6, // crash AT turn 6 (after turn-5 checkpoint already on disk)
	}
	crashed := runWithRecover(loop1, taskID, "summarize the codebase")
	if crashed {
		fmt.Println("(run 1 crashed as expected)")
	}
	cp, err := store.Load(taskID)
	if err != nil {
		fmt.Println("load after crash:", err)
		os.Exit(1)
	}
	if cp == nil {
		fmt.Println("ERROR: expected a checkpoint after the crash, found none")
		os.Exit(1)
	}
	fmt.Printf("checkpoint on disk: turn=%d  history_len=%d  file=%s\n\n",
		cp.Turn, len(cp.Messages), filepath.Join(dir, taskID+".json"))

	// --- Run 2: resumes from checkpoint. ---
	fmt.Println("--- Run 2: resumes from checkpoint ---")
	provider2 := &MockProvider{
		Script:      script,
		PanicAtTurn: -1,
		FailAtTurn:  -1,
	}
	loop2 := &Loop{
		Store:           store,
		Provider:        provider2,
		MaxTurns:        20,
		CheckpointEvery: 5,
		PanicAtTurn:     -1,
	}
	history, err := loop2.Run(context.Background(), taskID, "summarize the codebase")
	if err != nil {
		fmt.Println("run 2 error:", err)
		os.Exit(1)
	}
	fmt.Printf("run 2 completed: provider.Calls=%d  history_len=%d\n",
		provider2.Calls, len(history))
	fmt.Printf("  → only %d provider calls in run 2 means turns 0-%d were skipped.\n",
		provider2.Calls, cp.Turn-1)

	// Checkpoint should now be gone (Loop calls Clear on graceful exit).
	cp2, _ := store.Load(taskID)
	if cp2 == nil {
		fmt.Println("checkpoint was cleared after success — happy-path invariant holds.")
	} else {
		fmt.Printf("ERROR: checkpoint still on disk after success: %+v\n", cp2)
	}

	fmt.Println("\n=== final history ===")
	for i, m := range history {
		fmt.Printf("[%2d] %-9s %s\n", i, m.Role, m.Content)
	}
}

// runWithRecover wraps Loop.Run in a deferred recover so a panic in turn N
// doesn't take the demo binary down. Returns true if the recover fired.
func runWithRecover(l *Loop, taskID, userMessage string) (crashed bool) {
	defer func() {
		if r := recover(); r != nil {
			crashed = true
			fmt.Printf("recovered from panic: %v\n", r)
		}
	}()
	if _, err := l.Run(context.Background(), taskID, userMessage); err != nil {
		fmt.Println("run error:", err)
	}
	return false
}
