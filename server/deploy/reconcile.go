package deploy

import (
	"context"
	"fmt"
	"os"
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
		r.reconcileFromReport(ctx, exe)
	}
	r.reconcileSystemdUnits(ctx, exe)
	r.reconcileUsersAndGroups(ctx, exe)
	r.reconcileCrontabs(ctx, exe)
	r.checkExecutables(ctx, exe)

	return nil
}

// remoteExec runs commands on dst, escalating with sudo -n where needed.
type remoteExec struct {
	client *ssh.Client
	user   string
}

func (e *remoteExec) run(ctx context.Context, cmd string) (string, error) {
	t := &sshTarget{host: "", user: e.user, signer: nil}
	_ = t
	sess, err := e.client.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	out, err := sess.CombinedOutput(cmd)
	return string(out), err
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
	out, err := sess.CombinedOutput(cmd)
	return string(out), err
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
	out, pipInstallErr := exe.trySudo(ctx, fmt.Sprintf("pip3 install --disable-pip-version-check -q %s", singleQuoted(strings.Join(missing, " "))))
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
	npmOut, npmInstallErr := exe.trySudo(ctx, fmt.Sprintf("npm install -g %s", singleQuoted(strings.Join(missing, " "))))
	if npmInstallErr != nil {
		r.emitf("warn", "npm", "Some npm installs failed: %v\n%s", npmInstallErr, indentBlock(tail(npmOut)))
	} else {
		r.emit("success", "npm", "Global npm packages installed.")
	}
}

// --- systemd units ---

func (r *Run) reconcileSystemdUnits(ctx context.Context, exe *remoteExec) {
	units := localCustomUnits()
	if len(units) == 0 {
		r.emit("info", "systemd", "No custom systemd units on this VM to copy.")
		return
	}
	if r.Port != 0 {
		srcPort := detectListeningAppPorts(r.ProjectDir)
		if len(srcPort) > 0 && !containsInt(srcPort, r.Port) {
			r.emitf("info", "port", "Rewriting unit files from source port(s) %s to deployment port %d.",
				intJoin(srcPort, ", "), r.Port)
			for i := range units {
				for _, sp := range srcPort {
					newContent, n := replacePortInUnit(units[i].content, sp, r.Port)
					if n > 0 {
						units[i].content = newContent
						r.emitf("info", "port", "Rewrote %d port reference(s) in %s (%d → %d)", n, units[i].name, sp, r.Port)
					}
				}
			}
		}
	}
	r.emitf("info", "systemd", "Copying %d custom systemd unit(s): %s", len(units), strings.Join(unitNames(units), ", "))
	for _, u := range units {
		r.installUnit(ctx, exe, u)
	}
	if out, err := exe.trySudo(ctx, "systemctl daemon-reload"); err != nil {
		r.emitf("warn", "systemd", "daemon-reload failed: %v\n%s", err, indentBlock(tail(out)))
	}
}

func unitNames(units []localUnit) []string {
	var names []string
	for _, u := range units {
		names = append(names, u.name)
	}
	return names
}

func (r *Run) installUnit(ctx context.Context, exe *remoteExec, u localUnit) {
	// Copy the unit file.
	mkdir := "mkdir -p /etc/systemd/system"
	writeCmd := mkdir + " && tee /etc/systemd/system/" + u.name + " > /dev/null <<'UNIT_EOF'\n" + u.content + "\nUNIT_EOF"
	if _, err := exe.trySudo(ctx, writeCmd); err != nil {
		r.emitf("error", "systemd", "Failed to write unit %s: %v", u.name, err)
		return
	}
	if !u.enabled {
		r.emitf("info", "systemd", "Installed %s (not enabled at boot, matching source).", u.name)
		return
	}
	enableOut, enableErr := exe.trySudo(ctx, "systemctl enable "+u.name+" 2>&1")
	if enableErr != nil {
		r.emitf("warn", "systemd", "enable %s failed: %v\n%s", u.name, enableErr, indentBlock(tail(enableOut)))
		return
	}
	// Start it only if it was active on src.
	if u.active {
		startOut, startErr := exe.trySudo(ctx, "systemctl start "+u.name+" 2>&1")
		if startErr != nil {
			r.emitf("error", "systemd", "start %s failed: %v\n%s", u.name, startErr, indentBlock(tail(startOut)))
			return
		}
		r.emitf("success", "systemd", "%s enabled and started.", u.name)
	} else {
		r.emitf("success", "systemd", "%s enabled (was inactive on source; not started).", u.name)
	}
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

// replacePortInUnit rewrites occurrences of :port (in URLs, -addr flags,
// --port flags, etc.) inside a unit file and returns the new content plus the
// number of replacements.
func replacePortInUnit(content string, from, to int) (string, int) {
	n := 0
	re := regexp.MustCompile(":" + fmt.Sprint(from) + "([^0-9]|$)")
	out := re.ReplaceAllStringFunc(content, func(m string) string {
		n++
		return ":" + fmt.Sprint(to) + m[len(m)-1:]
	})
	return out, n
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
func (r *Run) reconcileFromReport(ctx context.Context, exe *remoteExec) {
	rep := r.Report
	if rep == nil {
		return
	}
	var missing []string
	for _, pkg := range rep.SystemPackages {
		out, err := exe.trySudo(ctx, "dpkg-query -W -f='${Status}' "+pkg+" 2>/dev/null")
		installed := err == nil && strings.Contains(out, "install ok installed")
		// Special case: uv isn't in older Ubuntu repos; check PATH instead.
		if pkg == "uv" && !installed {
			out2, err2 := exe.trySudo(ctx, "command -v uv")
			if err2 == nil && strings.TrimSpace(out2) != "" {
				installed = true
			}
		}
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
			r.emitf("warn", "apt", "Some installs failed: %v\n%s", err, indentBlock(tail(out)))
			// Retry one-by-one so one bad name doesn't sink the batch.
			for _, p := range missing {
				if out, err := exe.trySudo(ctx, "DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "+p); err != nil {
					r.emitf("warn", "apt", "%s: %v (%s)", p, err, firstLineStr(tail(out)))
				}
			}
		} else {
			r.emitf("success", "apt", "Installed %d package(s).", len(missing))
		}
	}

	// uv needs the standalone installer when apt can't provide it.
	if langNeeds(rep, "python", "uv") {
		out, err := exe.trySudo(ctx, "command -v uv || curl -LsSf https://astral.sh/uv/install.sh | sh")
		if err != nil {
			r.emitf("warn", "uv", "Could not ensure uv on destination: %v\n%s", err, indentBlock(tail(out)))
		} else if strings.Contains(out, "astral") || !strings.Contains(out, "/usr") {
			r.emit("info", "uv", "Installed uv via astral.sh installer (~/.local/bin/uv).")
		}
	}
}

func firstLineStr(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
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
		out, err := exe.trySudo(ctx, "ldd "+singleQuoted(e)+" 2>&1 | grep 'not found' || true")
		if err != nil {
			continue
		}
		if strings.TrimSpace(out) != "" {
			r.emitf("warn", "libs", "%s is missing shared libraries:\n%s", e, indentBlock(strings.TrimSpace(out)))
		}
	}
}
