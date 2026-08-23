package deploy

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// pipeline executes the full forklift. Each step emits events; the first
// failure ends the run with status failed.
func (r *Run) pipeline(c *execClient) {
	ctx, cancel := context.WithCancel(context.Background())
	cancelFuncs.Store(r, cancel)
	defer cancel()
	defer cancelFuncs.Delete(r)

	status := "failed"
	errMsg := ""
	defer func() {
		r.finish(status, errMsg)
	}()

	// Step 1: validate API key.
	r.emit("info", "validate", "Validating exe.dev API key…")
	email, err := c.Whoami(ctx)
	if err != nil {
		errMsg = err.Error()
		r.emit("error", "validate", errMsg)
		return
	}
	r.emitf("success", "validate", "Authenticated as %s", email)

	if r.DryRun {
		r.emit("warn", "dry-run", "Dry run: stopping before VM creation.")
		r.emitf("info", "plan", "Would create VM %q (image: %s)", r.VMName, imageLabel(r.Image))
		r.emitf("info", "plan", "Would rsync %s → same absolute path on the new VM", r.ProjectDir)
		r.emit("info", "plan", "Would reconcile packages/services/users by diffing src vs dst state")
		status = "success"
		return
	}

	// Step 2: refuse to clobber an existing VM.
	existing, err := c.Find(ctx, r.VMName)
	if err != nil {
		errMsg = err.Error()
		r.emit("error", "create", errMsg)
		return
	}
	if existing != nil {
		errMsg = fmt.Sprintf("VM %q already exists; pick a different name or delete it first", r.VMName)
		r.emit("error", "create", errMsg)
		return
	}

	// Step 3: ensure deploy SSH key exists.
	privPath, pubKey, err := ensureSSHKey()
	if err != nil {
		errMsg = err.Error()
		r.emit("error", "ssh-key", errMsg)
		return
	}

	// Step 4: create the VM.
	r.emitf("info", "create", "Creating VM %q (image: %s)…", r.VMName, imageLabel(r.Image))
	r.emit("info", "create", "This can take a minute or two.")
	if err := r.createVM(ctx, c, pubKey); err != nil {
		errMsg = err.Error()
		r.emit("error", "create", errMsg)
		return
	}

	host := r.VMName + ".exe.xyz"

	// Step 5: wait for SSH.
	target, err := r.waitForSSH(ctx, host, privPath)
	if err != nil {
		errMsg = err.Error()
		r.emit("error", "ssh", errMsg)
		return
	}

	client, err := target.dial()
	if err != nil {
		errMsg = fmt.Sprintf("SSH dial after bootstrap: %v", err)
		r.emit("error", "ssh", errMsg)
		return
	}
	defer client.Close()

	// Step 6: rsync project.
	if err := r.rsyncProject(ctx, target.user); err != nil {
		errMsg = err.Error()
		r.emit("error", "rsync", errMsg)
		return
	}

	// Step 7: reconcile system state.
	if err := r.reconcileState(ctx, client, target); err != nil {
		errMsg = err.Error()
		r.emit("error", "state", errMsg)
		return
	}

	url := "https://" + host + "/"
	r.emit("success", "done", "Deploy complete! 🎉")
	r.emitf("info", "done", "Your app should be available at %s (ports 3000-9999 are proxied automatically).", url)
	status = "success"
}

// isTimeoutErr recognizes the /exec 504 (30s limit) and client timeouts; both
// mean the `new` command may still be in flight server-side.
func isTimeoutErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "504") ||
		strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "took longer")
}

func imageLabel(image string) string {
	if image == "" {
		return "default exeuntu"
	}
	return image
}

// createVM creates the VM and installs our SSH key via a follow-up API call.
// exe.dev's `new` may take longer than the /exec 30s limit; on timeout we poll.
func (r *Run) createVM(ctx context.Context, c *execClient, pubKey string) error {
	privPath, _, _ := ensureSSHKey() // already validated in pipeline

	cmd := "new --name=" + r.VMName + " --no-email --json"
	if r.Image != "" {
		cmd += " --image=" + r.Image
	}
	cmd += " --setup-script=/dev/stdin"

	_, apiErr := c.execWithBody(ctx, cmd, setupScript(pubKey))
	if apiErr == nil {
		r.emit("success", "create", "VM created and first-boot setup script accepted.")
	} else if isTimeoutErr(apiErr) {
		r.emit("warn", "create", "Creation request timed out at the API layer (normal); polling for the VM…")
	} else {
		return fmt.Errorf("creating VM: %w", apiErr)
	}
	_ = privPath

	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		vm, err := c.Find(ctx, r.VMName)
		if err != nil {
			return fmt.Errorf("polling for new VM: %w", err)
		}
		if vm != nil && vm.Running() {
			r.emitf("success", "create", "VM %q is running.", vm.Name)
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	return fmt.Errorf("VM %q did not appear within 5 minutes", r.VMName)
}

// waitForSSH retries until SSH answers as some user.
func (r *Run) waitForSSH(ctx context.Context, host, privPath string) (*sshTarget, error) {
	deadline := time.Now().Add(3 * time.Minute)
	attempt := 0
	for time.Now().Before(deadline) {
		attempt++
		t, err := r.connectWithUserLadder(host, privPath)
		if err == nil {
			return t, nil
		}
		if attempt%6 == 1 { // ~every 30s
			r.emitf("info", "ssh", "Waiting for SSH on %s…", host)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	return nil, fmt.Errorf("SSH to %s never came up", host)
}

// finish marks the run complete and persists it.
func (r *Run) finish(status, errMsg string) {
	r.status = status
	r.errMsg = errMsg
	now := time.Now().UTC()
	r.finishedAt = &now
	if r.persistFn != nil {
		r.persistFn(r)
	}
	if status == "failed" {
		r.emitf("warn", "cleanup", "You can delete the partially-created VM from your exe.dev dashboard or lobby (`rm %s`).", r.VMName)
	}
}
