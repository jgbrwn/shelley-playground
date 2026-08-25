// Package deploy implements the "Deploy to new exe.dev VM" (forklift)
// pipeline: create a fresh exe.dev VM via the HTTPS /exec API, copy a project
// directory over with rsync, and reconcile system-level state (packages,
// services, users) by diffing live state between the source VM and the new
// destination VM.
package deploy

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DefaultImagePrefill is pre-filled in the deploy modal. An empty image field
// means "use the exeuntu default", exactly like creating a VM from the lobby.
const DefaultImagePrefill = "ghcr.io/ryanlewis/exeslim:latest"

// Event is one entry in a run's event log.
type Event struct {
	Time    string `json:"time"`
	Level   string `json:"level"` // info|warn|error|success|cmd
	Step    string `json:"step"`
	Message string `json:"message"`
}

// Run is a single in-flight (or finished) deploy pipeline.
type Run struct {
	ID             int64
	VMName         string
	Image          string
	ProjectDir     string // source path (on the playground VM)
	DstProjectDir  string // destination path under /home/exedev (derived)
	Port           int    // app port on dst; 0 = no specific port handling
	MakePublic     bool   // share the VM publicly after deploy
	DryRun         bool
	FullClone      bool   // full src→dst state diff (apt/pip/npm wholesale); default is minimal/project-scoped
	SkipSystemd    bool   // skip copying/creating systemd units on dst
	SourceOS       string // e.g. "linux/amd64", "darwin/arm64" — gates FullClone
	Report         *ProjectReport
	MarkdownReport string // copy-pastable dependency report (dry runs)

	// VMCreated is set once `new` has succeeded; failure UI should only
	// offer "delete the VM" when this is true.
	VMCreated bool

	status     string // running|success|failed
	errMsg     string
	finishedAt *time.Time
	persistFn  func(*Run) // set by the server to persist the run

	mu     sync.Mutex
	events []Event
	done   chan struct{} // closed when the pipeline finishes
}

// Status returns the run's current status.
func (r *Run) Status() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status == "" {
		return "running"
	}
	return r.status
}

// ErrMsg returns the failure message when status==failed.
func (r *Run) ErrMsg() string { return r.errMsg }

// FinishedAt returns when the run completed (nil while running).
func (r *Run) FinishedAt() *time.Time { return r.finishedAt }

// emitf is the formatted variant of emit.
func (r *Run) emitf(level, step, format string, args ...any) {
	r.emit(level, step, fmt.Sprintf(format, args...))
}

func (r *Run) emit(level, step, msg string) {
	e := Event{Time: time.Now().UTC().Format(time.RFC3339), Level: level, Step: step, Message: msg}
	r.mu.Lock()
	r.events = append(r.events, e)
	r.mu.Unlock()
	slog.Info("deploy", "run", r.ID, "step", step, "level", level, "msg", msg)
}

func (r *Run) snapshot() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Event, len(r.events))
	copy(out, r.events)
	return out
}

// Manager owns all runs; deploys are serialized (one at a time).
type Manager struct {
	mu      sync.Mutex
	current *Run             // nil when idle
	persist func(*Run) error // persists/updates a run row; optional (nil in tests)
}

func NewManager(persist func(*Run) error) *Manager { return &Manager{persist: persist} }

// Busy reports whether a deploy is currently running.
func (m *Manager) Busy() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.current != nil && !m.current.IsDone()
}

// Current returns the most recent run (may be finished), or nil.
func (m *Manager) Current() *Run {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.current
}

// Start launches a new deploy pipeline. It fails if another run is active or
// the parameters are invalid.
func (m *Manager) Start(apiKey, vmName, image, projectDir string, port int, makePublic, dryRun, fullClone, skipSystemd bool) (*Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current != nil && !m.current.IsDone() {
		return nil, fmt.Errorf("another deploy is already running")
	}
	m.current = nil // clear any finished run before starting a new one
	apiKey = strings.TrimSpace(apiKey)
	if err := ValidateAPIKey(apiKey); err != nil {
		return nil, err
	}
	if err := ValidateVMName(vmName); err != nil {
		return nil, err
	}
	image = strings.TrimSpace(image)
	if image != "" && (strings.HasPrefix(image, "-") || strings.ContainsAny(image, " \t\r\n")) {
		return nil, fmt.Errorf("image must be a single container image reference without whitespace or leading flags")
	}
	if port != 0 && (port < 3000 || port > 9999) {
		return nil, fmt.Errorf("app port must be between 3000 and 9999 (the proxied range), or empty")
	}
	abs, err := validateProjectDir(projectDir)
	if err != nil {
		return nil, err
	}
	if fullClone && !FullCloneSupported() {
		return nil, fmt.Errorf("full state clone requires a debian/ubuntu amd64 source VM; this host is %s", SourceOSLabel())
	}
	rep, err := AnalyzeProject(abs)
	if err != nil {
		return nil, fmt.Errorf("analyzing project: %w", err)
	}
	dstDir := "/home/exedev/" + filepath.Base(abs)
	run := &Run{
		VMName:        vmName,
		Image:         image,
		ProjectDir:    abs,
		DstProjectDir: dstDir,
		Port:          port,
		MakePublic:    makePublic,
		DryRun:        dryRun,
		FullClone:     fullClone,
		SkipSystemd:   skipSystemd,
		SourceOS:      SourceOSLabel(),
		Report:        rep,
		status:        "running",
		done:          make(chan struct{}),
	}
	if m.persist != nil {
		run.persistFn = func(rr *Run) { _ = m.persist(rr) }
		if err := m.persist(run); err != nil {
			return nil, err
		}
	}
	m.current = run
	go func() {
		defer close(run.done)
		run.pipeline(newExecClient(apiKey))
		// Don't clear m.current immediately: the SSE handler may not have
		// connected yet. Keep the finished run so /api/deploy/current can
		// replay its events. The run is cleared when the next deploy starts
		// (Start checks m.current != nil and rejects).
	}()
	return run, nil
}

// Wait blocks until the current run finishes.
func (r *Run) Wait(ctx context.Context) error {
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Done returns a channel closed when the run finishes.
func (r *Run) Done() <-chan struct{} { return r.done }

// IsDone reports whether the pipeline has finished (success or failure).
func (r *Run) IsDone() bool {
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}

// SnapshotEvents returns a copy of all events so far.
func (r *Run) SnapshotEvents() []Event { return r.snapshot() }

// DetectAppPortsForUI finds listening ports belonging to processes whose cwd
// is inside the given directory (the app we'd be forklifting). Used to
// prefill the modal's port field. Returns nil when nothing detected.
func DetectAppPortsForUI(_ context.Context, projectDir string) []int {
	if projectDir == "" {
		return nil
	}
	if info, err := os.Stat(projectDir); err != nil || !info.IsDir() {
		return nil
	}
	return detectListeningAppPorts(projectDir)
}

// Cancel requests cancellation of an in-flight run. The pipeline checks this
// between steps and kills child processes on stop.
var cancelFuncs sync.Map // map[*Run]context.CancelFunc

func (r *Run) Cancel() bool {
	if cf, ok := cancelFuncs.Load(r); ok {
		cf.(context.CancelFunc)()
		return true
	}
	return false
}

func ValidateAPIKey(key string) error {
	if key == "" {
		return fmt.Errorf("exe.dev API key is required")
	}
	if len(key) > 4096 || strings.ContainsAny(key, " \t\r\n\x00") {
		return fmt.Errorf("exe.dev API key has an invalid format")
	}
	if !strings.HasPrefix(key, "exe") || !strings.Contains(key, ".") {
		return fmt.Errorf("exe.dev API key must use the exe API token format")
	}
	return nil
}

func ValidateVMName(name string) error {
	if name == "" {
		return fmt.Errorf("VM name is required")
	}
	if len(name) > 40 {
		return fmt.Errorf("VM name too long (max 40 characters)")
	}
	if name[0] == '-' || name[len(name)-1] == '-' || strings.Contains(name, "--") {
		return fmt.Errorf("VM name must start and end with a letter or digit and use single hyphens between parts")
	}
	for _, c := range name {
		ok := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-'
		if !ok {
			return fmt.Errorf("VM name may only contain lowercase letters, digits and hyphens")
		}
	}
	return nil
}

func validateProjectDir(dir string) (string, error) {
	if dir == "" {
		return "", fmt.Errorf("project directory is required")
	}
	abs, err := filepathAbs(dir)
	if err != nil {
		return "", fmt.Errorf("invalid project directory: %w", err)
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolving project directory: %w", err)
	}
	abs = filepath.Clean(abs)
	if abs == string(filepath.Separator) {
		return "", fmt.Errorf("the filesystem root cannot be deployed as a project")
	}
	base := filepath.Base(abs)
	if base == "." || base == ".." || strings.ContainsAny(base, "\r\n\x00") {
		return "", fmt.Errorf("project directory has an unsafe basename")
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("project directory %s: %w", abs, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", abs)
	}
	return abs, nil
}

// outputResult captures a command's combined output and exit error.
type outputResult struct {
	stdout string
	err    error
}

func runCmd(ctx context.Context, dir string, env []string, name string, args ...string) outputResult {
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if env != nil {
		cmd.Env = env
	}
	out, err := cmd.CombinedOutput()
	return outputResult{stdout: strings.TrimSpace(string(out)), err: err}
}
