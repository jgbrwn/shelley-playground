package deploy

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// rsyncExcludes are never worth copying to a deployment VM.
var rsyncExcludes = []string{
	".git", // huge and irrelevant on dst; add a git init note instead
	"node_modules",
	"__pycache__",
	"*.pyc",
	".venv",
	"venv",
	".venv-backup",
}

func (r *Run) preflightDestination(ctx context.Context, exe *remoteExec) error {
	var lastErr error
	var lastOutput string
	for attempt := 1; attempt <= 10; attempt++ {
		out, err := exe.run(ctx, "if [ -r /etc/os-release ]; then cat /etc/os-release; else echo '/etc/os-release is not readable' >&2; exit 1; fi")
		if err == nil && strings.TrimSpace(out) != "" {
			lower := strings.ToLower(out)
			if !strings.Contains(lower, "id=ubuntu") && !strings.Contains(lower, "id=debian") &&
				!strings.Contains(lower, "id_like=ubuntu") && !strings.Contains(lower, "id_like=debian") {
				return fmt.Errorf("destination image is not Ubuntu/Debian-based:\n%s", strings.TrimSpace(out))
			}
			return r.preflightDestinationCommands(ctx, exe)
		}
		if err == nil {
			lastErr = fmt.Errorf("remote command returned empty output")
		} else {
			lastErr = err
		}
		lastOutput = out
		if attempt < 10 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
		}
	}
	return fmt.Errorf("could not read destination /etc/os-release after SSH became available: %w\n%s", lastErr, strings.TrimSpace(lastOutput))
}

func (r *Run) preflightDestinationCommands(ctx context.Context, exe *remoteExec) error {
	for _, command := range []string{"apt-get", "dpkg-query"} {
		if _, err := exe.run(ctx, "command -v "+command+" >/dev/null 2>&1"); err != nil {
			return fmt.Errorf("destination image is missing required command %s", command)
		}
	}
	if !r.SkipSystemd {
		if _, err := exe.run(ctx, "command -v systemctl >/dev/null 2>&1"); err != nil {
			return fmt.Errorf("destination image is missing systemctl; select Skip systemd or use a systemd-based image")
		}
	}
	if _, err := exe.run(ctx, "command -v rsync >/dev/null 2>&1"); err != nil {
		out, installErr := exe.trySudo(ctx, "DEBIAN_FRONTEND=noninteractive apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq rsync")
		if installErr != nil {
			return fmt.Errorf("installing destination rsync: %w\n%s", installErr, indentBlock(tail(out)))
		}
	}
	return nil
}

// prepareDestination refuses to mutate a pre-populated application tree from
// a custom image. The deployer currently creates new VMs rather than adopting
// existing deployments, so a non-empty destination is ownership-ambiguous.
func (r *Run) prepareDestination(ctx context.Context, exe *remoteExec) error {
	dst := singleQuoted(r.DstProjectDir)
	cmd := "if [ -L " + dst + " ]; then echo 'destination is a symlink'; exit 42; fi; " +
		"if [ -e " + dst + " ] && [ -n \"$(find " + dst + " -mindepth 1 -maxdepth 1 -print -quit)\" ]; then " +
		"echo 'destination already exists and is not empty'; exit 43; fi; " +
		"mkdir -p " + dst + "; test -w " + dst
	out, err := exe.run(ctx, cmd)
	if err != nil {
		return fmt.Errorf("destination preflight for %s failed: %w: %s", r.DstProjectDir, err, strings.TrimSpace(out))
	}
	return nil
}

// rsyncProject copies the project dir to /home/exedev/<basename> on the dst
// VM (not the full source path, which may be arbitrarily deep under
// /home/exedev/playground/...). venvs inside the project are excluded here and
// rebuilt in reconcile.
func (r *Run) rsyncProject(ctx context.Context, user string) error {
	dst := path.Join(user+"@"+r.VMName+".exe.xyz:", r.DstProjectDir)
	args := []string{"-az", "--delete", "-e", "ssh -o StrictHostKeyChecking=accept-new -i " + mustKeyPath()}
	for _, ex := range rsyncExcludes {
		args = append(args, "--exclude", ex)
	}
	args = append(args, strings.TrimSuffix(r.ProjectDir, "/")+"/", dst)

	r.emitf("cmd", "rsync", "rsync %s", strings.Join(args, " "))
	res := runCmd(ctx, "", nil, "rsync", args...)
	if res.err != nil {
		r.emitf("error", "rsync", "rsync failed: %v\n%s", res.err, indentBlock(res.stdout))
		return fmt.Errorf("rsync: %w", res.err)
	}
	r.emitf("success", "rsync", "Copied %s to %s:%s", r.ProjectDir, r.VMName+".exe.xyz", r.DstProjectDir)

	// Detect python venvs we skipped so reconcile can rebuild them.
	if venvs := findVenvs(r.ProjectDir); len(venvs) > 0 {
		r.emitf("info", "rsync", "Detected %d python virtualenv(s) that will be rebuilt on destination: %s",
			len(venvs), strings.Join(venvs, ", "))
	}
	return nil
}

func mustKeyPath() string {
	p, _, err := ensureSSHKey()
	if err != nil {
		panic(err) // key was already created earlier in the pipeline; unreachable
	}
	return p
}

func findVenvs(root string) []string {
	var out []string
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		name := filepath.Base(p)
		if (name == "venv" || name == ".venv") && isVenv(p) {
			out = append(out, p)
			return filepath.SkipDir
		}
		return nil
	})
	return out
}

func isVenv(dir string) bool {
	for _, marker := range []string{"pyvenv.cfg", "bin/pyvenv.cfg"} {
		if fileExists(filepath.Join(dir, marker)) {
			return true
		}
	}
	return false
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}
