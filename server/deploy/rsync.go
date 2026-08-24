package deploy

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// rsyncExcludes are never worth copying to a deployment VM.
var rsyncExcludes = []string{
	".git", // huge and irrelevant on dst; add a git init note instead
	"node_modules",
	"__pycache__",
	"*.pyc",
	".venv-backup",
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
			return filepath.SkipAll
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
