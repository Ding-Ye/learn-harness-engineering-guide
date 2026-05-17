package subagent

// SubTask and SubResult are the wire types between parent and child. They are
// deliberately small structs with JSON tags because the IPC contract is
// file-on-disk JSON, not Go-to-Go calls: the parent serializes a SubTask body
// into <WorkDir>/TASK.md and the child writes a SubResult back to
// <WorkDir>/RESULT.json. Keeping the structs small means the format can be
// hand-inspected, diff-ed, and reproduced from a shell prompt for debugging.
//
// We intentionally keep these in the library package (subagent), not under
// cmd/, so both the parent and the child can import the same Go types. Without
// that, the parent would marshal one shape and the child would unmarshal a
// slightly different shape, and the test matrix would be all about chasing
// drift between two type definitions.

// SubTask describes one unit of work to hand to a child process. The parent is
// responsible for ensuring WorkDir exists before spawn time; the spawner
// writes TASK.md (containing Instruction) into that directory and then the
// child reads it from os.Args[1].
type SubTask struct {
	// Name is a short label for logs and result correlation. Not used as a
	// filesystem path — WorkDir handles isolation.
	Name string `json:"name"`

	// Instruction is the body of TASK.md handed to the child. It is opaque
	// to the spawner: the child decides how to parse it. In s12 the child
	// implements a tiny "count unique words" tool plus two test-control
	// directives (`sleep:<dur>` and `crash:true`).
	Instruction string `json:"instruction"`

	// WorkDir is the isolated directory for this task. Each task gets its
	// own directory so two parallel tasks cannot read or overwrite each
	// other's TASK.md / RESULT.json. The spawner creates the directory if
	// it doesn't already exist (via MkdirAll, idempotent).
	WorkDir string `json:"work_dir"`
}

// SubResult is what the child writes to RESULT.json and what the parent reads
// back. The parent translates non-zero exit codes / timeouts into a SubResult
// with Success=false so the caller of Spawn() always gets a slice of
// SubResults of the same length as the input tasks — no separate error
// channel to drain.
type SubResult struct {
	// Name echoes back SubTask.Name so the parent can pair results to tasks
	// even when they arrive out of order from the worker pool.
	Name string `json:"name"`

	// Success is true only if the child wrote a result file with
	// "success": true AND exited 0. Any other path (non-zero exit, missing
	// RESULT.json, context cancel, stderr-only output) sets Success=false.
	Success bool `json:"success"`

	// Output is a free-form human-readable summary. The child's happy path
	// writes "<N> unique words"; the parent's failure path stuffs the
	// stderr tail (last 8 KiB) here so the caller has *something* to print.
	Output string `json:"output"`

	// Artifacts is a list of file paths the child produced inside WorkDir.
	// Always paths, not contents — the parent decides whether to ingest
	// them. In s12 the demo child writes no artifacts (returns nil), but
	// the field is on the wire so production children can hand back files.
	Artifacts []string `json:"artifacts"`

	// DurationMS is the wall-clock time the parent observed for this task,
	// measured from just-before-exec until just-after the child exited or
	// the context fired. Populated by the parent, not the child.
	DurationMS int64 `json:"duration_ms"`
}
