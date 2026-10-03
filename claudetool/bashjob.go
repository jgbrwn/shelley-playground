package claudetool

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"shelley.exe.dev/llm"
)

// BackgroundJob is a bash command that outlived its tool call. Its process
// is a small wrapper (see bashJobWrapper) that writes the command's exit
// status to ExitPath, so the status is recoverable even when Shelley is not
// the wrapper's parent.
type BackgroundJob struct {
	ID             string
	ConversationID string
	ToolUseID      string
	Command        string
	// PID is the wrapper's PID, which is also the job's process group ID.
	PID int
	// StartTime is the platform's process-start token. It distinguishes the
	// wrapper from a later process that reused its PID.
	StartTime uint64
	LogPath   string
	ExitPath  string
	StartedAt time.Time
}

// BackgroundJobs is told about every command bash moves to the background.
// exited is closed once the job has exited and its exit file is written.
// Implementations report the job's completion to its conversation.
type BackgroundJobs interface {
	Background(ctx context.Context, job BackgroundJob, exited <-chan struct{}) error
}

// bashJobWrapper runs $1 and atomically records its exit status in $2. It
// traps (rather than ignores) the polite termination signals so that
// `kill -- -PGID` stops the command but not the wrapper, which then records
// the status; children still get default signal dispositions.
const bashJobWrapper = `trap : HUP INT TERM
bash --login -c "$1"
s=$?
printf '%d\n' "$s" >"$2.tmp" && mv -f -- "$2.tmp" "$2"
exit "$s"`

// bashJobDir returns the directory holding job logs and exit files.
func bashJobDir() string {
	return filepath.Join(os.TempDir(), "shelley-jobs")
}

// newBashJobID returns a short random job ID.
func newBashJobID() string {
	var b [4]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// writeJobExitFile records status in path atomically, for wrappers that
// died before recording it themselves (SIGKILL).
func writeJobExitFile(path string, state *os.ProcessState) error {
	status := state.ExitCode()
	if ws, ok := state.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		status = 128 + int(ws.Signal())
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(status)+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// waitBashJob waits for cmd, a started job wrapper, ensures its exit file
// exists, and closes exited.
func waitBashJob(cmd *exec.Cmd, exitPath string, exited chan<- struct{}) {
	defer close(exited)
	cmd.Wait()
	if _, err := os.Stat(exitPath); errors.Is(err, os.ErrNotExist) {
		writeJobExitFile(exitPath, cmd.ProcessState)
	}
}

// ErrBackgroundJobGone reports that a job's wrapper has exited, or that
// its PID now names another process.
var ErrBackgroundJobGone = errors.New("background job is no longer running")

const jobNoticeTailLines = 20

// Notice describes j's outcome for its conversation once it has exited:
// its exit status, run time, log path, and the tail of its output.
func (j BackgroundJob) Notice() string {
	var b strings.Builder
	status, err := os.ReadFile(j.ExitPath)
	if err == nil {
		var elapsed time.Duration
		if fi, err := os.Stat(j.ExitPath); err == nil {
			elapsed = fi.ModTime().Sub(j.StartedAt).Round(time.Second)
		}
		fmt.Fprintf(&b, "Background job %s finished: exit %s, %s. Log: %s\n", j.ID, strings.TrimSpace(string(status)), elapsed, j.LogPath)
	} else {
		fmt.Fprintf(&b, "Background job %s lost (host rebooted or killed). Log: %s\n", j.ID, j.LogPath)
	}
	fmt.Fprintf(&b, "Command: %s\n", truncateLine(firstLine(j.Command)))
	if tail := logTail(j.LogPath, jobNoticeTailLines); tail != "" {
		b.WriteString("\n" + tail)
	}
	return strings.TrimRight(b.String(), "\n")
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " ..."
	}
	return s
}

// logTail returns at most the last n lines of the log at path, each
// truncated to maxLineLength.
func logTail(path string, n int) string {
	tail := strings.TrimRight(readTailString(path, int64(n*(maxLineLength+1))), "\n")
	if tail == "" {
		return ""
	}
	lines := strings.Split(tail, "\n")
	lines = lines[max(0, len(lines)-n):]
	for i, l := range lines {
		lines[i] = truncateLine(l)
	}
	return strings.Join(lines, "\n") + "\n"
}

// readTailString returns up to maxBytes from the end of the file (best-effort).
func readTailString(path string, maxBytes int64) string {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Sprintf("(could not open log: %v)", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return fmt.Sprintf("(could not stat log: %v)", err)
	}
	size := st.Size()
	if size == 0 {
		return ""
	}
	start := int64(0)
	truncated := false
	if size > maxBytes {
		start = size - maxBytes
		truncated = true
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return fmt.Sprintf("(could not seek log: %v)", err)
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return fmt.Sprintf("(could not read log: %v)", err)
	}
	out := string(b)
	if truncated {
		// Drop a possibly-partial first line.
		if i := strings.IndexByte(out, '\n'); i >= 0 && i < len(out)-1 {
			out = out[i+1:]
		}
	}
	return out
}

const logProgressInterval = 500 * time.Millisecond

// logProgressLoop reports the tail of the log at path to progress every
// logProgressInterval while it changes, and once more when stop closes.
func logProgressLoop(progress llm.ToolProgressFunc, toolID, toolName, path string, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(logProgressInterval)
	defer ticker.Stop()
	last := ""
	emit := func() {
		t := readTailString(path, progressMaxBytes)
		if t != last {
			last = t
			progress(llm.ToolProgress{
				ToolUseID: toolID,
				ToolName:  toolName,
				Output:    t,
			})
		}
	}
	for {
		select {
		case <-stop:
			emit()
			return
		case <-ticker.C:
			emit()
		}
	}
}
