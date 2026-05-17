package subagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// stderrTailBytes is how much of the child's stderr we capture for diagnostics
// when a task fails. 8 KiB is enough to hold a Go panic stack trace plus a few
// lines of context but small enough that we never blow memory by collecting
// stderr for hundreds of parallel workers.
const stderrTailBytes = 8 * 1024

// taskFilename is the well-known name the parent writes and the child reads.
// The child binary takes a single CLI arg — the work directory — and looks
// for TASK.md inside it. Keeping the name a constant means parent and child
// can't drift over time.
const taskFilename = "TASK.md"

// resultFilename is the well-known result file the child writes after doing
// its work. The parent reads JSON from here on exit-0. If the file is missing
// after a 0 exit we still produce a SubResult{Success: false} rather than
// pretending the child did its job.
const resultFilename = "RESULT.json"

// SubAgentSpawner runs a set of SubTasks as isolated child Go processes,
// communicating only via files inside each task's WorkDir. This is the
// upstream `sub-agent.md` L66-L145 pattern (Python `ThreadPoolExecutor` +
// `subprocess.run`) translated to Go using a semaphore-based worker pool and
// `exec.CommandContext` for per-task cancellation.
//
// The spawner is intentionally generic — it doesn't know what the child does,
// only that it produces a RESULT.json on success and a non-zero exit on
// failure. That means the same Spawner can drive any child binary as long as
// the binary respects the simple file-IPC contract.
type SubAgentSpawner struct {
	// ChildBinary is the path to the compiled child executable. The
	// spawner runs `ChildBinary <work_dir>` for each task. In production
	// this would be something like `python -m agent` (the upstream
	// default); in tests it's the path to `cmd/child` built by TestMain.
	ChildBinary string

	// MaxWorkers caps the number of children running in parallel. The
	// upstream `ThreadPoolExecutor(max_workers=4)` default lands here.
	// Values <= 0 are treated as 1 — we never go fully serial accidentally
	// just because the caller passed 0, but we also never default to
	// "unbounded" which would let a Spawn call fork-bomb the host.
	MaxWorkers int

	// Timeout is the per-task wall-clock limit. The spawner builds a
	// context.WithTimeout for each child and passes it to
	// exec.CommandContext, which sends SIGKILL (on Unix) or
	// TerminateProcess (on Windows) when the context fires. A value <= 0
	// means "no timeout" — we still wrap in the outer ctx, so the caller
	// can still cancel everything.
	Timeout time.Duration
}

// Spawn runs every task in tasks and returns one SubResult per task. Order of
// the output slice matches the order of the input slice, so callers can pair
// up by index without inspecting Name. The method blocks until either every
// task has finished OR the outer ctx fires, whichever happens first; when ctx
// fires, in-flight children are killed and remaining-but-not-started tasks
// yield SubResult{Success: false, Output: "context canceled"}.
//
// Spawn never returns an error itself — failures live inside the SubResult
// entries. That mirrors the upstream `as_completed` pattern (Python catches
// the exception per-task and converts it into a SubResult) and means the
// caller writes a simple loop over results, not an error-handling tree.
func (s *SubAgentSpawner) Spawn(ctx context.Context, tasks []SubTask) []SubResult {
	results := make([]SubResult, len(tasks))

	// Worker pool: a buffered channel acting as a semaphore. We don't use
	// a separate "worker goroutine" pool because each task is a long-lived
	// process anyway; one extra goroutine per task adds negligible
	// overhead and avoids the channel-of-tasks pattern that makes
	// cancellation messy.
	workers := s.MaxWorkers
	if workers <= 0 {
		workers = 1
	}
	sem := make(chan struct{}, workers)

	var wg sync.WaitGroup
	for i, t := range tasks {
		wg.Add(1)
		go func(i int, t SubTask) {
			defer wg.Done()
			// Acquire a slot. If ctx fires while we're waiting, bail
			// out with a synthetic failure result — never block past
			// cancellation.
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				results[i] = SubResult{
					Name:    t.Name,
					Success: false,
					Output:  "context canceled before task started: " + ctx.Err().Error(),
				}
				return
			}
			defer func() { <-sem }()

			results[i] = s.runOne(ctx, t)
		}(i, t)
	}

	wg.Wait()
	return results
}

// runOne is the per-task driver: prepare WorkDir, write TASK.md, exec the
// child, wait, classify the outcome. Everything that can fail produces a
// SubResult — we never panic out of this function.
func (s *SubAgentSpawner) runOne(parentCtx context.Context, t SubTask) SubResult {
	start := time.Now()
	res := SubResult{Name: t.Name}

	// 1. Ensure the WorkDir exists. MkdirAll is idempotent — safe to call
	// even if the caller already created it. We do NOT use os.TempDir
	// here even if WorkDir is empty: leaving it empty is a contract
	// violation and we'd rather fail loudly than silently scribble in /tmp.
	if t.WorkDir == "" {
		res.Output = "task.WorkDir is empty; refusing to run without an isolated directory"
		res.DurationMS = time.Since(start).Milliseconds()
		return res
	}
	if err := os.MkdirAll(t.WorkDir, 0o755); err != nil {
		res.Output = fmt.Sprintf("mkdir WorkDir: %v", err)
		res.DurationMS = time.Since(start).Milliseconds()
		return res
	}

	// 2. Write TASK.md. The body is exactly task.Instruction — the parent
	// adds no header, no envelope. That's the upstream contract and makes
	// the child's parser trivial (it can just look for `key:value` lines).
	taskPath := filepath.Join(t.WorkDir, taskFilename)
	if err := os.WriteFile(taskPath, []byte(t.Instruction), 0o644); err != nil {
		res.Output = fmt.Sprintf("write %s: %v", taskFilename, err)
		res.DurationMS = time.Since(start).Milliseconds()
		return res
	}

	// 3. Build a per-task context. If Timeout is set, wrap parentCtx with
	// a deadline. exec.CommandContext will kill the child when the
	// context fires. We always defer cancel() so the timer is released
	// promptly when the child exits early.
	ctx := parentCtx
	var cancel context.CancelFunc = func() {}
	if s.Timeout > 0 {
		ctx, cancel = context.WithTimeout(parentCtx, s.Timeout)
	}
	defer cancel()

	// 4. Exec the child. CommandContext (not Command) is critical: it
	// hooks up SIGKILL on context-done. The first arg after the binary
	// is the work directory — the child reads TASK.md from there.
	//
	// We capture stderr into a bounded buffer for diagnostics; stdout
	// is discarded because the contract is "write to RESULT.json", not
	// "print to stdout".
	cmd := exec.CommandContext(ctx, s.ChildBinary, t.WorkDir)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = nil // explicit: we don't read stdout

	runErr := cmd.Run()
	res.DurationMS = time.Since(start).Milliseconds()

	// 5. Classify the outcome. There are four possibilities:
	//
	//    a. ctx.Err() != nil AND it's deadline-exceeded → timeout.
	//    b. runErr != nil but ctx is fine → child exited non-zero.
	//    c. runErr == nil AND RESULT.json exists → happy path.
	//    d. runErr == nil AND RESULT.json missing → contract violation.
	//
	// We check ctx.Err() FIRST because exec.Run returns a generic
	// *exec.ExitError when the child is killed by signal, and that
	// looks the same as a normal crash unless we cross-check the
	// context state.
	if ctxErr := ctx.Err(); errors.Is(ctxErr, context.DeadlineExceeded) {
		res.Output = "timeout: child killed after " + s.Timeout.String() + "; stderr tail: " + tailString(stderr.Bytes())
		return res
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		res.Output = "canceled: parent context canceled; stderr tail: " + tailString(stderr.Bytes())
		return res
	}
	if runErr != nil {
		res.Output = fmt.Sprintf("child exited non-zero: %v; stderr tail: %s", runErr, tailString(stderr.Bytes()))
		return res
	}

	// 6. Happy path: read RESULT.json from WorkDir and copy fields onto
	// the SubResult. Note that we preserve our DurationMS over whatever
	// the child wrote — the parent's wall clock is the authoritative
	// measure of how long the task took.
	resultPath := filepath.Join(t.WorkDir, resultFilename)
	data, err := os.ReadFile(resultPath)
	if err != nil {
		res.Output = fmt.Sprintf("child exited 0 but %s missing: %v; stderr tail: %s", resultFilename, err, tailString(stderr.Bytes()))
		return res
	}
	var childRes SubResult
	if err := json.Unmarshal(data, &childRes); err != nil {
		res.Output = fmt.Sprintf("RESULT.json parse: %v; raw: %s", err, truncate(string(data), 512))
		return res
	}
	res.Success = childRes.Success
	res.Output = childRes.Output
	res.Artifacts = childRes.Artifacts
	return res
}

// tailString returns the last stderrTailBytes of p as a string. We rely on
// this to keep the failure-mode SubResult.Output bounded; otherwise a child
// that spams stderr would balloon the parent's memory.
func tailString(p []byte) string {
	if len(p) <= stderrTailBytes {
		return string(p)
	}
	return "..." + string(p[len(p)-stderrTailBytes:])
}

// truncate clips s to n bytes for display. We use it on RESULT.json parse
// errors so a corrupt 5 MB file doesn't get printed in full to a log line.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
