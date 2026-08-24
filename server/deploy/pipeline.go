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
		r.emitf("info", "plan", "Would rsync %s → %s on the new VM", r.ProjectDir, r.DstProjectDir)
		r.emitf("info", "plan", "Would install project dependencies on destination (%s)", depInstallPlan(r.Report))
		if r.SkipSystemd {
			r.emit("info", "plan", "Would skip systemd unit reconciliation (declined).")
		} else {
			r.emit("info", "plan", "Would copy/create systemd units on destination.")
		}
		if r.Port != 0 {
			r.emitf("info", "plan", "Would route the VM's proxy to app port %d and configure services for it", r.Port)
		}
		if r.MakePublic {
			r.emit("info", "plan", "Would share the VM publicly (share set-public)")
		}
		if r.FullClone && FullCloneSupported() {
			r.emit("info", "plan", "Would run FULL state clone (all apt/pip/npm packages diffed src→dst).")
		} else {
			mode := "MINIMAL (project-scoped)"
			if r.FullClone {
				mode = "minimal (project-scoped) — full clone unavailable on this host (" + SourceOSLabel() + ")"
			}
			r.emitf("info", "plan", "State reconciliation would be %s.", mode)
		}
		// Dependency report — always generated.
		r.emitDependencyReport()
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

	r.VMCreated = true
	host := r.VMName + ".exe.xyz"

	// Step 4b: route the exe.dev proxy at the requested app port. Must happen
	// before set-public so the public share targets the right port.
	if r.Port != 0 {
		r.emitf("info", "port", "Routing proxy for %s to app port %d…", host, r.Port)
		if err := c.SharePort(ctx, r.VMName, r.Port); err != nil {
			errMsg = err.Error()
			r.emitf("error", "port", "Failed to route proxy: %v", err)
			return
		}
		r.emitf("success", "port", "Proxy routed to port %d.", r.Port)
	}

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

	// Step 8: share publicly if requested. New VMs are private by default,
	// so nothing to do otherwise.
	if r.MakePublic {
		r.emit("info", "share", "Sharing VM publicly…")
		if err := c.SetPublic(ctx, r.VMName, true); err != nil {
			errMsg = err.Error()
			r.emitf("error", "share", "Failed to make public: %v", err)
			return
		}
		r.emit("success", "share", "VM is now publicly accessible.")
	}

	url := "https://" + host + "/"
	if r.Port != 0 && r.Port != 8000 {
		url = fmt.Sprintf("https://%s:%d/", host, r.Port)
	}
	r.emit("success", "done", "Deploy complete! 🎉")
	r.emitf("info", "done", "Your app should be available at %s", url)
	status = "success"
}

// emitDependencyReport prints what AnalyzeProject found for the project.
func (r *Run) emitDependencyReport() {
	rep := r.Report
	if rep == nil {
		return
	}
	if len(rep.Languages) == 0 {
		r.emit("info", "report", "No languages/manifests detected in the project directory.")
	} else {
		for _, l := range rep.Languages {
			r.emitf("info", "report", "%s via %s (from %s)", l.Name, l.Manager, l.Manifest)
		}
	}
	if len(rep.SystemPackages) > 0 {
		r.emitf("info", "report", "System packages needed on destination: %s", strings.Join(rep.SystemPackages, ", "))
	} else {
		r.emit("info", "report", "No extra system packages detected.")
	}
	for _, e := range rep.Executables {
		r.emitf("info", "report", "Built executable: %s (libs will be checked on destination)", e)
	}
	for _, n := range rep.Notes {
		r.emitf("info", "report", "Note: %s", n)
	}
	r.MarkdownReport = BuildMarkdownReport(rep)
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

// depInstallPlan summarizes the dependency-install commands that would run
// on the destination, for display in the dry-run plan.
func depInstallPlan(rep *ProjectReport) string {
	if rep == nil || len(rep.Languages) == 0 {
		return "none detected"
	}
	var parts []string
	for _, lang := range rep.Languages {
		switch {
		case lang.Name == "python" && lang.Manager == "uv":
			parts = append(parts, "uv sync")
		case lang.Name == "python":
			parts = append(parts, "pip install")
		case lang.Name == "node" && lang.Manager == "pnpm":
			parts = append(parts, "pnpm install")
		case lang.Name == "node" && lang.Manager == "yarn":
			parts = append(parts, "yarn install")
		case lang.Name == "node":
			parts = append(parts, "npm install")
		case lang.Name == "go":
			parts = append(parts, "go build")
		case lang.Name == "rust":
			parts = append(parts, "cargo build")
		case lang.Name == "ruby":
			parts = append(parts, "bundle install")
		case lang.Name == "php":
			parts = append(parts, "composer install")
		case lang.Name == "java" && lang.Manager == "maven":
			parts = append(parts, "mvn install")
		case lang.Name == "java" && lang.Manager == "gradle":
			parts = append(parts, "gradle build")
		}
	}
	if len(parts) == 0 {
		return "none detected"
	}
	return strings.Join(parts, ", ")
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

// finish marks the run complete.
func (r *Run) finish(status, errMsg string) {
	r.status = status
	r.errMsg = errMsg
	now := time.Now().UTC()
	r.finishedAt = &now
	if status == "failed" && r.VMCreated {
		r.emitf("warn", "cleanup",
			"VM %q was created but the deploy failed partway. You can delete it with `rm %s` via the exe.dev API, or keep it to retry manually.", r.VMName, r.VMName)
	}
}
