package deploy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"
)

// reconcileState diffs system-level state between src (localhost) and dst and
// replays the delta on dst: apt packages, pip/npm globals, systemd units,
// users, groups, crontabs. Everything runs as target.user (exedev preferred;
// sudo -n is used for privileged commands).
func (r *Run) reconcileState(ctx context.Context, client *ssh.Client, target *sshTarget) error {
	exe := &remoteExec{client: client, user: target.user}

	// If we're root but exedev should exist, create it so paths align.
	if target.user == rootUser {
		r.emit("info", "state", "Connected as root; creating exedev user to align paths…")
		if out, err := exe.run(ctx, ensureExedevUser(0, 0, "")); err != nil {
			r.emitf("warn", "state", "Could not create exedev user: %v\n%s", err, indentBlock(out))
		}
	}

	if r.FullClone {
		r.emit("info", "state", "Full state clone enabled: diffing all packages between source and destination…")
		r.reconcilePackages(ctx, exe)
		r.reconcilePythonGlobals(ctx, exe)
		r.reconcileNpmGlobals(ctx, exe)
	} else {
		r.emit("info", "state", "Minimal (project-scoped) mode: installing only what the project needs.")
		if err := r.reconcileFromReport(ctx, exe); err != nil {
			return err
		}
	}
	if !r.SkipSystemd {
		if err := r.reconcileSystemdUnits(ctx, exe); err != nil {
			return err
		}
	} else {
		r.emit("info", "systemd", "Skipping systemd unit reconciliation (declined by user).")
	}
	if r.FullClone {
		r.reconcileUsersAndGroups(ctx, exe)
		r.reconcileCrontabs(ctx, exe)
	}
	r.checkExecutables(ctx, exe)

	return nil
}

// remoteExec runs commands on dst, escalating with sudo -n where needed.
type remoteExec struct {
	client *ssh.Client
	user   string
}

func (e *remoteExec) run(ctx context.Context, cmd string) (string, error) {
	sess, err := e.client.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	return combinedOutputContext(ctx, sess, cmd)
}

// sudo wraps a command if the session user isn't root.
func (e *remoteExec) sudo(cmd string) string {
	if e.user == rootUser {
		return cmd
	}
	return "sudo -n sh -c " + singleQuoted(cmd)
}

func (e *remoteExec) trySudo(ctx context.Context, cmd string) (string, error) {
	out, err := e.run(ctx, e.sudo(cmd))
	return out, err
}

func (e *remoteExec) trySudoIgnoreErr(ctx context.Context, cmd string) (string, error) {
	out, err := e.run(ctx, e.sudo(cmd))
	return out, err
}

func (e *remoteExec) trySudoStdin(ctx context.Context, cmd, stdin string) (string, error) {
	out, err := e.runStdin(ctx, e.sudo(cmd), stdin)
	return out, err
}

func (e *remoteExec) runStdin(ctx context.Context, cmd, stdin string) (string, error) {
	sess, err := e.client.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	sess.Stdin = strings.NewReader(stdin)
	return combinedOutputContext(ctx, sess, cmd)
}

func combinedOutputContext(ctx context.Context, session *ssh.Session, cmd string) (string, error) {
	type result struct {
		out []byte
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := session.CombinedOutput(cmd)
		done <- result{out: out, err: err}
	}()
	select {
	case result := <-done:
		return string(result.out), result.err
	case <-ctx.Done():
		_ = session.Close()
		result := <-done
		if len(result.out) > 0 {
			return string(result.out), ctx.Err()
		}
		return "", ctx.Err()
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func setDiff(src, dst map[string]bool) []string {
	var missing []string
	for k := range src {
		if !dst[k] {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	return missing
}

// --- packages ---

func (r *Run) reconcilePackages(ctx context.Context, exe *remoteExec) {
	r.emit("info", "apt", "Comparing installed apt packages…")

	srcOut, err := dpkgListLocal()
	if err != nil {
		r.emitf("warn", "apt", "Could not list local packages: %v", err)
		return
	}
	dstOut, ok2 := exe.trySudo(ctx, "dpkg-query -W -f='${Package}\\n' 2>/dev/null")
	if ok2 != nil {
		r.emit("warn", "apt", "Could not list destination packages; skipping apt reconciliation.")
		return
	}
	src := parseLines(srcOut)
	dst := parseLines(dstOut)
	missing := setDiff(src, dst)
	if len(missing) == 0 {
		r.emitf("success", "apt", "Destination already has all %d source packages.", len(src))
		return
	}
	r.emitf("info", "apt", "%d package(s) present here but not there; installing by name…", len(missing))

	// Batch install in chunks of 100 to keep command lines sane.
	const chunk = 100
	var failed []string
	for i := 0; i < len(missing); i += chunk {
		end := i + chunk
		if end > len(missing) {
			end = len(missing)
		}
		batch := missing[i:end]
		cmd := fmt.Sprintf("DEBIAN_FRONTEND=noninteractive apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq %s", strings.Join(batch, " "))
		out, installErr := exe.trySudo(ctx, cmd)
		if installErr != nil {
			r.emitf("warn", "apt", "Batch starting at %q failed (packages may not exist in dst's repos): %v", batch[0], installErr)
			r.emit("cmd", "apt", indentBlock(tail(out)))
			failed = append(failed, batch...)
		} else {
			r.emitf("success", "apt", "Installed %d package(s).", len(batch))
		}
	}
	if len(failed) > 0 {
		r.emitf("warn", "apt", "Could not install %d package(s): %s", len(failed), strings.Join(failed, " "))
		r.emit("warn", "apt", "These may be named differently on the destination's base image or no longer exist.")
	}
}

// --- python globals ---

func (r *Run) reconcilePythonGlobals(ctx context.Context, exe *remoteExec) {
	if !commandExistsLocal("pip3") {
		return
	}
	srcFreeze, err := pipFreezeLocal()
	if err != nil || len(parseLines(srcFreeze)) == 0 {
		return
	}
	dstHasPip, pipErr := exe.trySudo(ctx, "command -v pip3")
	if pipErr != nil || strings.TrimSpace(dstHasPip) == "" {
		r.emit("warn", "pip", "Destination has no pip3; skipping global python packages.")
		return
	}
	dstFreeze, _ := exe.trySudoIgnoreErr(ctx, "pip3 freeze --disable-pip-version-check 2>/dev/null")
	missing := freezeDiff(srcFreeze, dstFreeze)
	if len(missing) == 0 {
		r.emit("success", "pip", "Global python packages match.")
		return
	}
	r.emitf("info", "pip", "Installing %d global python package(s)…", len(missing))
	out, pipInstallErr := exe.trySudo(ctx, "pip3 install --disable-pip-version-check -q "+quoteShellArgs(missing))
	if pipInstallErr != nil {
		r.emitf("warn", "pip", "Some global pip installs failed: %v\n%s", pipInstallErr, indentBlock(tail(out)))
	} else {
		r.emit("success", "pip", "Global python packages installed.")
	}
}

// --- npm globals ---

func (r *Run) reconcileNpmGlobals(ctx context.Context, exe *remoteExec) {
	if !commandExistsLocal("npm") {
		return
	}
	srcOut, err := npmGlobalsLocal()
	if err != nil || len(parseNpmList(srcOut)) == 0 {
		return
	}
	dstNpmOut, npmListErr := exe.trySudo(ctx, "npm ls -g --depth=0 2>/dev/null")
	if npmListErr != nil {
		r.emit("warn", "npm", "Destination has no npm; skipping global npm packages.")
		return
	}
	missing := setDiff(parseNpmList(srcOut), parseNpmList(dstNpmOut))
	if len(missing) == 0 {
		r.emit("success", "npm", "Global npm packages match.")
		return
	}
	r.emitf("info", "npm", "Installing %d global npm package(s)…", len(missing))
	npmOut, npmInstallErr := exe.trySudo(ctx, "npm install -g "+quoteShellArgs(missing))
	if npmInstallErr != nil {
		r.emitf("warn", "npm", "Some npm installs failed: %v\n%s", npmInstallErr, indentBlock(tail(npmOut)))
	} else {
		r.emit("success", "npm", "Global npm packages installed.")
	}
}

// --- systemd units ---

type systemdPlan struct {
	units     []localUnit
	generated bool
	start     appStart
}

func (r *Run) planSystemdUnits() (systemdPlan, error) {
	units, err := localProjectUnits(r.ProjectDir)
	if err != nil {
		return systemdPlan{}, fmt.Errorf("discovering project systemd units: %w", err)
	}
	if len(units) == 0 {
		start, err := detectAppStart(r.ProjectDir, r.Report, r.Port)
		if err != nil {
			return systemdPlan{}, err
		}
		unit := generatedSystemdUnit(filepath.Base(r.DstProjectDir), r.DstProjectDir, start)
		return systemdPlan{units: []localUnit{unit}, generated: true, start: start}, nil
	}

	for i := range units {
		units[i].content = strings.ReplaceAll(units[i].content, r.ProjectDir, r.DstProjectDir)
		if r.Port != 0 {
			for _, sourcePort := range detectListeningAppPorts(r.ProjectDir) {
				units[i].content, _ = replacePortInUnit(units[i].content, sourcePort, r.Port)
			}
		}
	}
	return systemdPlan{units: units}, nil
}

func (r *Run) reconcileSystemdUnits(ctx context.Context, exe *remoteExec) error {
	plan, err := r.planSystemdUnits()
	if err != nil {
		return fmt.Errorf("planning systemd service: %w", err)
	}
	if plan.generated {
		r.emitf("info", "systemd", "No project systemd unit found; generating %s from %s: %s",
			plan.units[0].name, plan.start.source, plan.start.command)
	} else {
		r.emitf("info", "systemd", "Found %d project-related unit(s) on source: %s",
			len(plan.units), strings.Join(unitNames(plan.units), ", "))
	}

	for _, unit := range plan.units {
		if err := writeSystemdUnit(ctx, exe, unit); err != nil {
			return err
		}
	}
	if out, err := exe.trySudo(ctx, "systemctl daemon-reload"); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %w\n%s", err, indentBlock(tail(out)))
	}
	for _, unit := range plan.units {
		if err := r.applySystemdUnitState(ctx, exe, unit); err != nil {
			return err
		}
	}
	return nil
}

func unitNames(units []localUnit) []string {
	var names []string
	for _, u := range units {
		names = append(names, u.name)
	}
	return names
}

var safeUnitNameRE = regexp.MustCompile(`^[A-Za-z0-9_.@-]+\.(service|timer|socket|path)$`)

func writeSystemdUnit(ctx context.Context, exe *remoteExec, unit localUnit) error {
	if !safeUnitNameRE.MatchString(unit.name) {
		return fmt.Errorf("unsafe systemd unit name %q", unit.name)
	}
	cmd := "umask 022; tmp=$(mktemp /etc/systemd/system/.shelley-deploy.XXXXXX); cat > \"$tmp\"; chmod 0644 \"$tmp\"; mv \"$tmp\" /etc/systemd/system/" + unit.name
	out, err := exe.trySudoStdin(ctx, cmd, unit.content)
	if err != nil {
		return fmt.Errorf("writing systemd unit %s: %w\n%s", unit.name, err, indentBlock(tail(out)))
	}
	return nil
}

func (r *Run) applySystemdUnitState(ctx context.Context, exe *remoteExec, unit localUnit) error {
	if unit.enabled {
		out, err := exe.trySudo(ctx, "systemctl enable "+unit.name+" 2>&1")
		if err != nil {
			return fmt.Errorf("enabling systemd unit %s: %w\n%s", unit.name, err, indentBlock(tail(out)))
		}
	}
	if unit.active {
		out, err := exe.trySudo(ctx, "systemctl start "+unit.name+" 2>&1")
		if err != nil {
			return fmt.Errorf("starting systemd unit %s: %w\n%s", unit.name, err, indentBlock(tail(out)))
		}
	}
	switch {
	case unit.enabled && unit.active:
		r.emitf("success", "systemd", "%s enabled and started.", unit.name)
	case unit.enabled:
		r.emitf("success", "systemd", "%s enabled (inactive on source; not started).", unit.name)
	case unit.active:
		r.emitf("success", "systemd", "%s started (disabled on source; not enabled).", unit.name)
	default:
		r.emitf("info", "systemd", "Installed %s (disabled and inactive, matching source).", unit.name)
	}
	return nil
}

// --- users/groups/crontabs ---

var skipUsers = map[string]bool{
	"root": true, "daemon": true, "bin": true, "sys": true, "lp": true,
	"mail": true, "news": true, "uucp": true, "proxy": true, "www-data": true,
	"backup": true, "list": true, "irc": true, "_ssh": true, "sshd": true,
	"sync": true, "shutdown": true, "halt": true, "operator": true,
	"games": true, "man": true, "gnats": true, "systemd-network": true,
	"systemd-resolve": true, "messagebus": true, "exedev": true,
	"ubuntu": true, "nobody": true,
}

func (r *Run) reconcileUsersAndGroups(ctx context.Context, exe *remoteExec) {
	srcPasswd, err := osReadFile("/etc/passwd")
	if err != nil {
		return
	}
	dstPasswd, passwdErr := exe.trySudo(ctx, "cat /etc/passwd")
	if passwdErr != nil {
		return
	}
	missing := setDiff(parseUserNames(srcPasswd), parseUserNames(dstPasswd))
	var realMissing []string
	for _, u := range missing {
		if !skipUsers[u] {
			realMissing = append(realMissing, u)
		}
	}
	for _, u := range realMissing {
		entry := findPasswdEntry(srcPasswd, u)
		r.emitf("info", "users", "Creating user %q present on source but not destination.", u)
		if out, useraddErr := exe.trySudo(ctx, "useradd "+passwdHomeFlag(entry)+singleQuoted(u)); useraddErr != nil {
			r.emitf("warn", "users", "useradd %s failed: %v\n%s", u, useraddErr, indentBlock(tail(out)))
		}
	}
	if len(realMissing) > 0 {
		r.emitf("success", "users", "Created %d user(s).", len(realMissing))
	}
}

func passwdHomeFlag(entry string) string {
	// entry like: name:x:1000:1000::/home/name:/bin/bash
	parts := strings.Split(entry, ":")
	if len(parts) >= 6 && parts[5] != "" {
		return "-d " + singleQuoted(parts[5]) + " "
	}
	return ""
}

func (r *Run) reconcileCrontabs(ctx context.Context, exe *remoteExec) {
	srcCron, err := localUserCrontab()
	if err != nil || strings.TrimSpace(srcCron) == "" {
		return
	}
	r.emit("info", "cron", "Source VM has a user crontab; copying…")
	cronOut, cronErr := exe.trySudoStdin(ctx, "crontab -u "+exedevUser+" -", srcCron)
	if cronErr != nil {
		r.emitf("warn", "cron", "Failed to install crontab: %v\n%s", cronErr, indentBlock(tail(cronOut)))
	} else {
		r.emitf("success", "cron", "Crontab installed for %s.", exedevUser)
	}
}

// containsInt reports whether n is in list.
func containsInt(list []int, n int) bool {
	for _, v := range list {
		if v == n {
			return true
		}
	}
	return false
}

func intJoin(list []int, sep string) string {
	parts := make([]string, len(list))
	for i, v := range list {
		parts[i] = fmt.Sprint(v)
	}
	return strings.Join(parts, sep)
}

// replacePortInUnit rewrites standalone numeric port references while
// preserving surrounding punctuation/whitespace.
func replacePortInUnit(content string, from, to int) (string, int) {
	re := regexp.MustCompile(`(^|[^0-9])` + regexp.QuoteMeta(fmt.Sprint(from)) + `([^0-9]|$)`)
	count := len(re.FindAllStringIndex(content, -1))
	if count == 0 {
		return content, 0
	}
	return re.ReplaceAllString(content, "${1}"+fmt.Sprint(to)+"${2}"), count
}

// detectListeningAppPorts finds TCP ports of processes whose cwd is inside
// the project directory — i.e. the app we're forklifting. Returns empty if
// nothing detected.
func detectListeningAppPorts(projectDir string) []int {
	out, err := runLocal("ss", "-tlnp")
	if err != nil {
		return nil
	}
	var ports []int
	seen := map[int]bool{}
	for _, line := range strings.Split(out, "\n") {
		m := listenRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		port := atoiSafe(m[1])
		if port < 1024 || seen[port] {
			continue
		}
		pid := atoiSafe(pidFromListenLine(line))
		if pid == 0 {
			continue
		}
		cwd, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid))
		if err != nil {
			continue
		}
		if cwd == projectDir || strings.HasPrefix(cwd, projectDir+"/") {
			seen[port] = true
			ports = append(ports, port)
		}
	}
	sort.Ints(ports)
	return ports
}

var listenRe = regexp.MustCompile(`:(\d+)\s.*users:\[\("`)

func pidFromListenLine(line string) string {
	i := strings.Index(line, "pid=")
	if i < 0 {
		return ""
	}
	rest := line[i+4:]
	j := strings.IndexAny(rest, ",)")
	if j < 0 {
		return rest
	}
	return rest[:j]
}

func atoiSafe(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

// reconcileFromReport installs only the system packages the analyzed project
// needs — not the whole source VM's package list. Also handles language-level
// deps that need a live destination (uv install path, go/cargo builds).
func (r *Run) reconcileFromReport(ctx context.Context, exe *remoteExec) error {
	rep := r.Report
	if rep == nil {
		return nil
	}
	var missing []string
	for _, pkg := range rep.SystemPackages {
		// uv is not consistently packaged by Ubuntu/Debian. Provision it as
		// the deploy user below so its binary lands in that user's PATH.
		if pkg == "uv" {
			continue
		}
		out, err := exe.trySudo(ctx, "dpkg-query -W -f='${Status}' "+pkg+" 2>/dev/null")
		installed := err == nil && strings.Contains(out, "install ok installed")
		if !installed {
			missing = append(missing, pkg)
		}
	}
	if len(missing) == 0 {
		if len(rep.SystemPackages) > 0 {
			r.emitf("success", "apt", "All %d project package(s) already present.", len(rep.SystemPackages))
		}
	} else {
		r.emitf("info", "apt", "Installing %d project package(s): %s", len(missing), strings.Join(missing, " "))
		cmd := fmt.Sprintf("DEBIAN_FRONTEND=noninteractive apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq %s", strings.Join(missing, " "))
		out, err := exe.trySudo(ctx, cmd)
		if err != nil {
			r.emitf("warn", "apt", "Batch install failed; retrying packages individually: %v\n%s", err, indentBlock(tail(out)))
			var failed []string
			for _, p := range missing {
				if out, err := exe.trySudo(ctx, "DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "+p); err != nil {
					r.emitf("error", "apt", "%s: %v (%s)", p, err, firstLineStr(tail(out)))
					failed = append(failed, p)
				}
			}
			if len(failed) > 0 {
				return fmt.Errorf("installing required system packages failed: %s", strings.Join(failed, ", "))
			}
		}
		r.emitf("success", "apt", "Installed %d package(s).", len(missing))
	}

	// uv needs the standalone installer when the distro doesn't package it.
	// Run it as the deploy user, not through sudo/root.
	if langNeeds(rep, "python", "uv") {
		out, err := exe.run(ctx, "command -v uv >/dev/null 2>&1 || curl -LsSf https://astral.sh/uv/install.sh | sh")
		if err != nil {
			return fmt.Errorf("installing uv for deploy user: %w\n%s", err, indentBlock(tail(out)))
		} else {
			r.emit("success", "uv", "uv is available for the deploy user.")
		}
	}

	// Install the project's own language-level dependencies on the
	// destination. rsync excluded node_modules and venvs, so the dst has
	// the source code but none of the installed deps. Rebuild them here.
	return r.installProjectDeps(ctx, exe)
}

func firstLineStr(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// installProjectDeps runs the project's own dependency install/build command
// on the destination. rsync excluded node_modules and venvs, so the dst has
// source code but no installed deps. This rebuilds them in-place.
//
// All commands run as the deploy user (not sudo) in the project directory.
func (r *Run) installProjectDeps(ctx context.Context, exe *remoteExec) error {
	rep := r.Report
	if rep == nil || r.ProjectDir == "" {
		return nil
	}
	cd := "cd " + singleQuoted(r.DstProjectDir)
	for _, lang := range rep.Languages {
		switch {
		case lang.Name == "python" && lang.Manager == "uv":
			r.emit("info", "deps", "Installing python dependencies (uv sync)…")
			out, err := exe.run(ctx, cd+" && uv sync --quiet 2>&1")
			if err != nil {
				return fmt.Errorf("uv sync: %w\n%s", err, indentBlock(tail(out)))
			}
			r.emit("success", "deps", "Python dependencies installed (uv sync).")
		case lang.Name == "python":
			r.emit("info", "deps", "Installing python dependencies (pip)…")
			createVenv := "if [ ! -d .venv ]; then python3 -m venv .venv; fi"
			var install string
			switch {
			case fileExists(filepath.Join(r.ProjectDir, "requirements.txt")):
				install = ".venv/bin/pip install --quiet -r requirements.txt"
			case fileExists(filepath.Join(r.ProjectDir, "pyproject.toml")), fileExists(filepath.Join(r.ProjectDir, "setup.py")):
				install = ".venv/bin/pip install --quiet ."
			case fileExists(filepath.Join(r.ProjectDir, "Pipfile")):
				install = ".venv/bin/pip install --quiet pipenv && PIPENV_VENV_IN_PROJECT=1 .venv/bin/pipenv sync"
			default:
				return fmt.Errorf("python was detected but no supported dependency manifest was found")
			}
			out, err := exe.run(ctx, cd+" && "+createVenv+" && "+install+" 2>&1")
			if err != nil {
				return fmt.Errorf("pip install: %w\n%s", err, indentBlock(tail(out)))
			}
			r.emit("success", "deps", "Python dependencies installed (pip).")
		case lang.Name == "node" && lang.Manager == "pnpm":
			r.emit("info", "deps", "Installing node dependencies (pnpm install)…")
			if out, err := exe.trySudo(ctx, "command -v pnpm >/dev/null 2>&1 || npm install -g pnpm"); err != nil {
				return fmt.Errorf("installing pnpm: %w\n%s", err, indentBlock(tail(out)))
			}
			out, err := exe.run(ctx, cd+" && pnpm install --frozen-lockfile 2>&1")
			if err != nil {
				return fmt.Errorf("pnpm install: %w\n%s", err, indentBlock(tail(out)))
			}
			r.emit("success", "deps", "Node dependencies installed (pnpm).")
			if err := r.buildNodeProject(ctx, exe, "pnpm"); err != nil {
				return err
			}
		case lang.Name == "node" && lang.Manager == "yarn":
			r.emit("info", "deps", "Installing node dependencies (yarn install)…")
			if out, err := exe.trySudo(ctx, "command -v yarn >/dev/null 2>&1 || npm install -g yarn"); err != nil {
				return fmt.Errorf("installing yarn: %w\n%s", err, indentBlock(tail(out)))
			}
			out, err := exe.run(ctx, cd+" && yarn install --frozen-lockfile 2>&1")
			if err != nil {
				return fmt.Errorf("yarn install: %w\n%s", err, indentBlock(tail(out)))
			}
			r.emit("success", "deps", "Node dependencies installed (yarn).")
			if err := r.buildNodeProject(ctx, exe, "yarn"); err != nil {
				return err
			}
		case lang.Name == "node" && lang.Manager == "bun":
			r.emit("info", "deps", "Installing node dependencies (bun install)…")
			if out, err := exe.run(ctx, "command -v bun >/dev/null 2>&1 || curl -fsSL https://bun.sh/install | bash"); err != nil {
				return fmt.Errorf("installing bun: %w\n%s", err, indentBlock(tail(out)))
			}
			out, err := exe.run(ctx, cd+" && ~/.bun/bin/bun install --frozen-lockfile 2>&1")
			if err != nil {
				return fmt.Errorf("bun install: %w\n%s", err, indentBlock(tail(out)))
			}
			r.emit("success", "deps", "Node dependencies installed (bun).")
			if err := r.buildNodeProject(ctx, exe, "~/.bun/bin/bun"); err != nil {
				return err
			}
		case lang.Name == "node":
			installCommand := "npm install"
			installLabel := "npm"
			if fileExists(filepath.Join(r.ProjectDir, "package-lock.json")) {
				installCommand = "npm ci"
				installLabel = "npm ci"
			}
			r.emitf("info", "deps", "Installing node dependencies (%s)…", installCommand)
			out, err := exe.run(ctx, cd+" && "+installCommand+" 2>&1")
			if err != nil {
				return fmt.Errorf("%s: %w\n%s", installCommand, err, indentBlock(tail(out)))
			}
			r.emitf("success", "deps", "Node dependencies installed (%s).", installLabel)
			if err := r.buildNodeProject(ctx, exe, "npm"); err != nil {
				return err
			}
		case lang.Name == "go":
			r.emit("info", "deps", "Building go project…")
			out, err := exe.run(ctx, cd+" && go build ./... 2>&1")
			if err != nil {
				return fmt.Errorf("go build: %w\n%s", err, indentBlock(tail(out)))
			}
			r.emit("success", "deps", "Go project built.")
		case lang.Name == "rust":
			r.emit("info", "deps", "Building rust project…")
			out, err := exe.run(ctx, cd+" && cargo build --release 2>&1")
			if err != nil {
				return fmt.Errorf("cargo build: %w\n%s", err, indentBlock(tail(out)))
			}
			r.emit("success", "deps", "Rust project built.")
		case lang.Name == "ruby":
			r.emit("info", "deps", "Installing ruby dependencies (bundle install)…")
			out, err := exe.run(ctx, cd+" && bundle install 2>&1")
			if err != nil {
				return fmt.Errorf("bundle install: %w\n%s", err, indentBlock(tail(out)))
			}
			r.emit("success", "deps", "Ruby dependencies installed.")
		case lang.Name == "php":
			r.emit("info", "deps", "Installing php dependencies (composer install)…")
			out, err := exe.run(ctx, cd+" && composer install --no-interaction 2>&1")
			if err != nil {
				return fmt.Errorf("composer install: %w\n%s", err, indentBlock(tail(out)))
			}
			r.emit("success", "deps", "PHP dependencies installed.")
		case lang.Name == "java" && lang.Manager == "maven":
			r.emit("info", "deps", "Building java project (mvn install)…")
			out, err := exe.run(ctx, cd+" && mvn -q install 2>&1")
			if err != nil {
				return fmt.Errorf("maven build: %w\n%s", err, indentBlock(tail(out)))
			}
			r.emit("success", "deps", "Java project built (maven).")
		case lang.Name == "java" && lang.Manager == "gradle":
			r.emit("info", "deps", "Building java project (gradle build)…")
			out, err := exe.run(ctx, cd+" && ./gradlew build 2>&1")
			if err != nil {
				return fmt.Errorf("gradle build: %w\n%s", err, indentBlock(tail(out)))
			}
			r.emit("success", "deps", "Java project built (gradle).")
		}
	}
	return nil
}

func (r *Run) buildNodeProject(ctx context.Context, exe *remoteExec, manager string) error {
	if !packageHasScript(filepath.Join(r.ProjectDir, "package.json"), "build") {
		return nil
	}
	r.emitf("info", "deps", "Running node build script (%s run build)…", manager)
	out, err := exe.run(ctx, "cd "+singleQuoted(r.DstProjectDir)+" && "+manager+" run build 2>&1")
	if err != nil {
		return fmt.Errorf("node build: %w\n%s", err, indentBlock(tail(out)))
	}
	r.emit("success", "deps", "Node build completed.")
	return nil
}

func langNeeds(rep *ProjectReport, lang, mgr string) bool {
	for _, l := range rep.Languages {
		if l.Name == lang && l.Manager == mgr {
			return true
		}
	}
	return false
}

// checkExecutables verifies built binaries found in the project have their
// shared libraries satisfied on dst; missing libs are reported (not guessed).
func (r *Run) checkExecutables(ctx context.Context, exe *remoteExec) {
	if r.Report == nil || len(r.Report.Executables) == 0 {
		return
	}
	for _, e := range r.Report.Executables {
		remotePath := r.DstProjectDir + "/" + strings.TrimPrefix(filepath.ToSlash(e), "/")
		out, err := exe.run(ctx, "ldd "+singleQuoted(remotePath)+" 2>&1 | grep 'not found' || true")
		if err != nil {
			continue
		}
		if strings.TrimSpace(out) != "" {
			r.emitf("warn", "libs", "%s is missing shared libraries:\n%s", e, indentBlock(strings.TrimSpace(out)))
		}
	}
}
