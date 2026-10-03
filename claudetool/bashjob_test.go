package claudetool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"shelley.exe.dev/llm"
)

type backgroundedJob struct {
	job    BackgroundJob
	exited <-chan struct{}
}

// recordingJobs is a BackgroundJobs that hands each job to the test.
type recordingJobs chan backgroundedJob

func (r recordingJobs) Background(ctx context.Context, job BackgroundJob, exited <-chan struct{}) error {
	r <- backgroundedJob{job, exited}
	return nil
}

// gate is a FIFO a command blocks reading until the test releases it.
type gate string

func newGate(t *testing.T) gate {
	t.Helper()
	p := filepath.Join(t.TempDir(), "gate")
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Fatal(err)
	}
	return gate(p)
}

// wait is the shell snippet that blocks on the gate.
func (g gate) wait() string { return fmt.Sprintf("read -r _ < %q", string(g)) }

// readerAlive reports whether some process is blocked on the gate.
func (g gate) readerAlive(t *testing.T) bool {
	t.Helper()
	f, err := os.OpenFile(string(g), os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if errors.Is(err, syscall.ENXIO) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	return true
}

func (g gate) release(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(string(g), []byte("go\n"), 0); err != nil {
		t.Fatal(err)
	}
}

func waitClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func TestBashBackgroundsLongCommand(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir()) // keep the job log out of /tmp
	jobs := make(recordingJobs, 1)
	tool := (&BashTool{
		WorkingDir:      NewMutableWorkingDir(t.TempDir()),
		Env:             ShelleyEnv{ConversationID: "conv-1"},
		Jobs:            jobs,
		BackgroundAfter: 50 * time.Millisecond,
	}).Tool()
	g := newGate(t)
	ready := newGate(t)
	command := fmt.Sprintf(
		"exec 3<>%q; echo started; printf ready > %q; read -r _ <&3; echo finished; exit 3",
		string(g),
		string(ready),
	)
	input, _ := json.Marshal(bashInput{Command: command})
	ctx, cancel := context.WithCancel(WithToolUseID(t.Context(), "toolu_1"))
	defer cancel()

	out := tool.Run(ctx, input)
	if out.Error != nil {
		t.Fatalf("backgrounded command reported error: %v", out.Error)
	}
	bg := <-jobs
	job := bg.job
	t.Cleanup(func() {
		select {
		case <-bg.exited:
			return
		default:
		}
		if err := job.Kill(); err != nil && !errors.Is(err, ErrBackgroundJobGone) {
			t.Errorf("kill background job during cleanup: %v", err)
		}
		select {
		case <-bg.exited:
		case <-time.After(5 * time.Second):
			if err := syscall.Kill(-job.PID, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				t.Errorf("force-kill background job during cleanup: %v", err)
			}
			<-bg.exited
		}
	})
	if job.ConversationID != "conv-1" || job.ToolUseID != "toolu_1" || job.Command != command {
		t.Errorf("job = %+v, want conversation, tool use, and command recorded", job)
	}
	display := bashDisplayData(t, out.Display)
	if display.ExitCode != nil {
		t.Errorf("ExitCode = %d, want unknown while running", *display.ExitCode)
	}
	if display.Background == nil || display.Background.JobID != job.ID || display.Background.LogPath != job.LogPath || display.Background.PGID != job.PID {
		t.Errorf("display.Background = %+v, want job %+v", display.Background, job)
	}
	text := out.LLMContent[0].Text
	for _, want := range []string{job.ID, job.LogPath, "kill -- -" + strconv.Itoa(job.PID), "do not poll"} {
		if !strings.Contains(text, want) {
			t.Errorf("result %q does not contain %q", text, want)
		}
	}

	// Wait until the command has written its first line and opened the gate
	// before cancelling the turn; process startup can exceed the background
	// threshold on loaded CI runners.
	readyResult := make(chan error, 1)
	go func() {
		f, err := os.Open(string(ready))
		if err != nil {
			readyResult <- err
			return
		}
		defer f.Close()
		output, err := io.ReadAll(f)
		if err == nil && strings.TrimSpace(string(output)) != "ready" {
			err = fmt.Errorf("readiness signal = %q, want ready", output)
		}
		readyResult <- err
	}()
	select {
	case err := <-readyResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("backgrounded command did not reach the gate")
	}
	if output, err := os.ReadFile(job.LogPath); err != nil {
		t.Fatal(err)
	} else if !strings.Contains(string(output), "started\n") {
		t.Fatalf("background job log %q does not contain its startup output", output)
	}

	// Cancelling the turn no longer affects a backgrounded job.
	cancel()
	if !g.readerAlive(t) {
		t.Fatal("backgrounded job died when its turn was cancelled")
	}
	select {
	case <-bg.exited:
		t.Fatal("exited closed while job still running")
	default:
	}

	g.release(t)
	waitClosed(t, bg.exited, "job exit")
	notice := job.Notice()
	for _, want := range []string{"Background job " + job.ID + " finished: exit 3", job.LogPath, "started\nfinished"} {
		if !strings.Contains(notice, want) {
			t.Errorf("notice %q does not contain %q", notice, want)
		}
	}
}

func TestBashBackgroundFieldSkipsForegroundWait(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	jobs := make(recordingJobs, 1)
	// The default 60s threshold applies; background=true must not wait for it.
	tool := (&BashTool{WorkingDir: NewMutableWorkingDir(t.TempDir()), Jobs: jobs}).Tool()
	// A command that exits at once still becomes a job and reports through
	// the notice, not the tool result.
	input, _ := json.Marshal(bashInput{Command: "echo hi", Background: true})

	done := make(chan llm.ToolOut, 1)
	go func() { done <- tool.Run(t.Context(), input) }()
	var out llm.ToolOut
	select {
	case out = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("background=true waited for the foreground threshold")
	}
	if out.Error != nil {
		t.Fatalf("error = %v", out.Error)
	}
	bg := <-jobs
	if d := bashDisplayData(t, out.Display); d.Background == nil || d.Background.JobID != bg.job.ID {
		t.Errorf("display.Background = %+v, want job %s", d.Background, bg.job.ID)
	}
	waitClosed(t, bg.exited, "job exit")
	if notice := bg.job.Notice(); !strings.Contains(notice, "finished: exit 0") || !strings.Contains(notice, "hi") {
		t.Errorf("notice = %q, want exit 0 with output", notice)
	}
}

func TestBashCancelKillsForegroundCommand(t *testing.T) {
	tool := (&BashTool{WorkingDir: NewMutableWorkingDir(t.TempDir())}).Tool()
	g := newGate(t)
	// The command reports its PID through a second FIFO; reading it blocks
	// until the command is running.
	ready := newGate(t)
	input, _ := json.Marshal(bashInput{Command: fmt.Sprintf("echo $$ > %q; %s", string(ready), g.wait())})
	ctx, cancel := context.WithCancel(t.Context())

	done := make(chan error)
	go func() { done <- tool.Run(ctx, input).Error }()
	b, err := os.ReadFile(string(ready))
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	st, err := processStartTime(pid)
	if err != nil {
		t.Fatal(err)
	}
	commandExited, err := BackgroundJob{PID: pid, StartTime: st}.Exited()
	if err != nil {
		t.Fatal(err)
	}

	cancel()
	if err := <-done; err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want cancellation", err)
	}
	waitClosed(t, commandExited, "command to be killed")
}

func TestBackgroundJobRecovery(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "j.log")
	if err := os.WriteFile(logPath, []byte("one\ntwo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	job := BackgroundJob{ID: "j", Command: "make", LogPath: logPath, ExitPath: filepath.Join(dir, "j.exit"), StartedAt: time.Now()}

	t.Run("running then finished", func(t *testing.T) {
		g := newGate(t)
		cmd := exec.Command("bash", "-c", g.wait())
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		go cmd.Wait()
		j := job
		j.PID = cmd.Process.Pid
		st, err := processStartTime(j.PID)
		if err != nil {
			t.Fatal(err)
		}
		j.StartTime = st
		exited, err := j.Exited()
		if err != nil {
			t.Fatal(err)
		}
		select {
		case <-exited:
			t.Fatal("exited closed while process is running")
		default:
		}
		g.release(t)
		waitClosed(t, exited, "process exit")
	})

	t.Run("pid reused", func(t *testing.T) {
		j := job
		j.PID = os.Getpid()
		j.StartTime = 1 // not this process's start time
		exited, err := j.Exited()
		if err != nil {
			t.Fatal(err)
		}
		waitClosed(t, exited, "reused pid to count as exited")
	})

	t.Run("lost", func(t *testing.T) {
		if got := job.Notice(); !strings.HasPrefix(got, "Background job j lost (host rebooted or killed). Log: "+logPath) || !strings.HasSuffix(got, "one\ntwo") {
			t.Errorf("notice = %q", got)
		}
	})

	t.Run("finished", func(t *testing.T) {
		if err := os.WriteFile(job.ExitPath, []byte("0\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := job.Notice(); !strings.HasPrefix(got, "Background job j finished: exit 0, ") {
			t.Errorf("notice = %q", got)
		}
	})
}
