// Parent CLI: scans a directory for `task-*.md` files, builds SubTasks out of
// them, and runs them through SubAgentSpawner. Prints one line per result
// summarizing name, success, output, and observed duration.
//
// Usage:
//
//	go build ./cmd/child
//	go build ./cmd/parent
//	mkdir -p /tmp/s12demo/{a,b,c}
//	echo "instruction: count these words" > /tmp/s12demo/a/task-a.md
//	echo "instruction: count these too"   > /tmp/s12demo/b/task-b.md
//	echo "instruction: short one"         > /tmp/s12demo/c/task-c.md
//	./parent -child ./child -root /tmp/s12demo
//
// The parent does NOT try to enforce isolation by topology: it just hands the
// task body straight to the child binary. The child decides how to parse it.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	subagent "learn-harness-engineering-guide/s12-sub-agent"
)

func main() {
	child := flag.String("child", "", "path to compiled child binary (required)")
	root := flag.String("root", "", "root directory; each subdir of root is one task's WorkDir (required)")
	workers := flag.Int("workers", 4, "max parallel children")
	timeout := flag.Duration("timeout", 30*time.Second, "per-task timeout")
	flag.Parse()

	if *child == "" || *root == "" {
		fmt.Fprintln(os.Stderr, "usage: parent -child <path> -root <dir> [-workers N] [-timeout D]")
		os.Exit(2)
	}

	// Sanity-check the child exists. We don't try to verify it's a Go
	// binary or that it speaks the right protocol — exec will fail
	// loudly enough if not.
	if _, err := os.Stat(*child); err != nil {
		log.Fatalf("child binary not found: %v", err)
	}

	// Build the task set: every direct subdirectory of root is one task.
	// The task's Instruction is the contents of the FIRST `task-*.md`
	// file we find inside that subdirectory; we ignore everything else.
	// That keeps the demo small while still exercising real
	// multi-task fan-out.
	tasks, err := buildTasks(*root)
	if err != nil {
		log.Fatalf("buildTasks: %v", err)
	}
	if len(tasks) == 0 {
		log.Fatalf("no tasks found under %s", *root)
	}

	spawner := &subagent.SubAgentSpawner{
		ChildBinary: *child,
		MaxWorkers:  *workers,
		Timeout:     *timeout,
	}
	results := spawner.Spawn(context.Background(), tasks)

	// Print results in the same order as the input tasks. The demo
	// format is two lines per task: a status header and the output body.
	for i, r := range results {
		status := "FAIL"
		if r.Success {
			status = "OK"
		}
		fmt.Printf("[%s] task=%s name=%s duration=%dms\n", status, tasks[i].Name, r.Name, r.DurationMS)
		if r.Output != "" {
			fmt.Printf("       output: %s\n", strings.TrimSpace(r.Output))
		}
		if len(r.Artifacts) > 0 {
			fmt.Printf("       artifacts: %v\n", r.Artifacts)
		}
	}
}

// buildTasks scans rootDir and returns one SubTask per subdirectory that
// contains a file matching `task-*.md`. The subdirectory itself is used as
// the WorkDir (each task is isolated in its own dir, which is the upstream
// contract). The task's Name is the subdirectory basename; the Instruction
// is the file contents.
func buildTasks(rootDir string) ([]subagent.SubTask, error) {
	entries, err := os.ReadDir(rootDir)
	if err != nil {
		return nil, err
	}
	var tasks []subagent.SubTask
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(rootDir, e.Name())
		matches, err := filepath.Glob(filepath.Join(dir, "task-*.md"))
		if err != nil || len(matches) == 0 {
			continue
		}
		body, err := os.ReadFile(matches[0])
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", matches[0], err)
		}
		tasks = append(tasks, subagent.SubTask{
			Name:        e.Name(),
			Instruction: string(body),
			WorkDir:     dir,
		})
	}
	return tasks, nil
}
