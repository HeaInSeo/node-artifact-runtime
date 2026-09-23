package runtimehelper

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// adoptedSleepers returns the pids of `sleep` processes, live or zombie, whose
// parent is this test process, i.e. orphans reparented to the subreaper.
func adoptedSleepers(t *testing.T) map[int]bool {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Fatalf("read /proc: %v", err)
	}
	self := os.Getpid()
	pids := map[int]bool{}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if err != nil {
			continue
		}
		stat := string(raw)
		open, closing := strings.IndexByte(stat, '('), strings.LastIndexByte(stat, ')')
		if open < 0 || closing < open {
			continue
		}
		fields := strings.Fields(stat[closing+1:])
		if len(fields) < 2 {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		if ppid == self && stat[open+1:closing] == "sleep" {
			pids[pid] = true
		}
	}
	return pids
}

func pollUntil(t *testing.T, limit time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", limit, what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestExecuteCommandReapsAdoptedOrphansWhileMainChildRuns is the C2
// regression for #10: orphans adopted by the subreaper must be reaped while
// the main child is still running, not only after it exits.
func TestExecuteCommandReapsAdoptedOrphansWhileMainChildRuns(t *testing.T) {
	tmpDir := t.TempDir()
	readyPath := filepath.Join(tmpDir, "ready")
	stopPath := filepath.Join(tmpDir, "stop")
	script := fmt.Sprintf(
		"for i in 1 2 3 4; do (sleep 0.3 &); done; : > %q; while [ ! -e %q ]; do sleep 0.02; done",
		readyPath, stopPath,
	)
	before := adoptedSleepers(t)

	resultCh := make(chan commandResult, 1)
	go func() {
		resultCh <- executeCommand(context.Background(), Config{
			Command:             []string{"sh", "-c", script},
			ShutdownGracePeriod: time.Second,
		}, nil)
	}()
	stopped := false
	stopMain := func() {
		if !stopped {
			stopped = true
			if err := os.WriteFile(stopPath, nil, 0o600); err != nil {
				t.Errorf("write stop file: %v", err)
			}
		}
	}
	defer stopMain()

	pollUntil(t, 5*time.Second, "main child to start its orphans", func() bool {
		_, err := os.Stat(readyPath)
		return err == nil
	})
	orphans := map[int]bool{}
	pollUntil(t, 2*time.Second, "orphans to be reparented to the subreaper", func() bool {
		for pid := range adoptedSleepers(t) {
			if !before[pid] {
				orphans[pid] = true
			}
		}
		return len(orphans) >= 4
	})
	pollUntil(t, 3*time.Second, "adopted orphans to be reaped while the main child runs", func() bool {
		current := adoptedSleepers(t)
		for pid := range orphans {
			if current[pid] {
				return false
			}
		}
		return true
	})
	select {
	case result := <-resultCh:
		t.Fatalf("main child exited before the orphans were checked: %+v", result)
	default:
	}

	stopMain()
	select {
	case result := <-resultCh:
		if result.Err != nil || result.ExitCode != ExitSuccess || result.NotClean {
			t.Fatalf("executeCommand() = %+v, want clean success", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("main child did not exit after the stop file was written")
	}
}

// TestExecuteCommandKeepsDirectChildStatusDuringTermination is the C1
// regression for #10: reaping adopted descendants while the process group is
// being terminated must not consume the direct child's exit status.
func TestExecuteCommandKeepsDirectChildStatusDuringTermination(t *testing.T) {
	script := "trap 'exit 7' TERM; while :; do (sleep 0.01 &); sleep 0.01; done"
	for i := 0; i < 5; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		result := executeCommand(ctx, Config{
			Command:             []string{"sh", "-c", script},
			ShutdownGracePeriod: 2 * time.Second,
		}, nil)
		cancel()
		if !result.TimedOut || result.Killed {
			t.Fatalf("iteration %d: executeCommand() = %+v, want timeout without SIGKILL", i, result)
		}
		var exitErr *commandExitError
		if !errors.As(result.Err, &exitErr) || exitErr.ExitCode() != 7 {
			t.Fatalf("iteration %d: Err = %v, want the direct child's exit status 7", i, result.Err)
		}
	}
}

// TestExecuteCommandKeepsDirectChildExitCodeWithOrphanChurn checks that the
// direct child's own exit code survives many adopted descendants being reaped
// by the same reaper during the run.
func TestExecuteCommandKeepsDirectChildExitCodeWithOrphanChurn(t *testing.T) {
	result := executeCommand(context.Background(), Config{
		Command:             []string{"sh", "-c", "for i in $(seq 30); do (true &); done; sleep 0.2; exit 3"},
		ShutdownGracePeriod: time.Second,
	}, nil)
	if result.NotClean || result.ExitCode != 3 {
		t.Fatalf("executeCommand() = %+v, want exit code 3", result)
	}
	if result.Err == nil || result.Err.Error() != "exit status 3" {
		t.Fatalf("Err = %v, want \"exit status 3\"", result.Err)
	}
}

// TestExecuteCommandOverlappingRunsKeepOwnDirectChildStatus checks that
// overlapping executeCommand calls share one reaper: no call may consume
// another call's direct-child status and leave it blocked.
func TestExecuteCommandOverlappingRunsKeepOwnDirectChildStatus(t *testing.T) {
	const runs = 6
	type outcome struct {
		want   int
		result commandResult
	}
	outcomes := make(chan outcome, runs)
	for i := 0; i < runs; i++ {
		want := 10 + i
		script := fmt.Sprintf("for i in $(seq 10); do (true &); done; sleep 0.0%d; exit %d", i, want)
		go func() {
			outcomes <- outcome{want: want, result: executeCommand(context.Background(), Config{
				Command:             []string{"sh", "-c", script},
				ShutdownGracePeriod: time.Second,
			}, nil)}
		}()
	}
	timeout := time.After(10 * time.Second)
	for i := 0; i < runs; i++ {
		select {
		case got := <-outcomes:
			if got.result.NotClean || got.result.ExitCode != got.want {
				t.Fatalf("executeCommand() = %+v, want exit code %d", got.result, got.want)
			}
		case <-timeout:
			t.Fatalf("only %d of %d overlapping runs returned; a direct-child status was consumed by another run", i, runs)
		}
	}
}

func TestExecuteCommandCopiesOutputToNonFileWriters(t *testing.T) {
	var stdout, stderr strings.Builder
	result := executeCommand(context.Background(), Config{
		Command: []string{"sh", "-c", "echo out; echo err >&2"},
		Stdout:  &stdout,
		Stderr:  &stderr,
	}, nil)
	if result.Err != nil || result.ExitCode != ExitSuccess {
		t.Fatalf("executeCommand() = %+v, want success", result)
	}
	if stdout.String() != "out\n" || stderr.String() != "err\n" {
		t.Fatalf("stdout = %q, stderr = %q, want \"out\\n\" and \"err\\n\"", stdout.String(), stderr.String())
	}

	var combined strings.Builder
	result = executeCommand(context.Background(), Config{
		Command: []string{"sh", "-c", "echo out; echo err >&2"},
		Stdout:  &combined,
		Stderr:  &combined,
	}, nil)
	if result.Err != nil || combined.String() != "out\nerr\n" {
		t.Fatalf("combined output = %q, Err = %v, want \"out\\nerr\\n\"", combined.String(), result.Err)
	}
}

func TestExecuteCommandReportsSignaledDirectChild(t *testing.T) {
	result := executeCommand(context.Background(), Config{
		Command: []string{"sh", "-c", "kill -KILL $$"},
	}, nil)
	var exitErr *commandExitError
	if !errors.As(result.Err, &exitErr) || result.Err.Error() != "signal: killed" || result.ExitCode != -1 {
		t.Fatalf("executeCommand() = %+v, want \"signal: killed\" with exit code -1", result)
	}
}
