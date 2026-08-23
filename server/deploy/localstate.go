package deploy

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Helpers for inspecting the *source* (playground) VM's live state.

func runLocal(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

func commandExistsLocal(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func dpkgListLocal() (string, error) {
	return runLocal("dpkg-query", "-W", "-f=${Package}\n")
}

func pipFreezeLocal() (string, error) {
	out, err := runLocal("pip3", "freeze", "--disable-pip-version-check")
	if err != nil && strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("pip3 freeze: %w", err)
	}
	return out, nil
}

func npmGlobalsLocal() (string, error) {
	return runLocal("npm", "ls", "-g", "--depth=0")
}

func osReadFile(p string) (string, error) {
	b, err := os.ReadFile(p)
	return string(b), err
}

func localUserCrontab() (string, error) {
	return runLocal("crontab", "-l")
}

// localCustomUnits returns non-default systemd unit files in
// /etc/systemd/system, with their enabled/active state.
type localUnit struct {
	name    string // e.g. "myapp.service"
	content string
	enabled bool
	active  bool
}

var builtinUnitPrefixes = []string{
	"multi-user.target.wants/", "sockets.target.wants/", "timers.target.wants/",
	"default.target.wants/", "sysinit.target.wants/", "graphical.target.wants/",
	"network-online.target.wants/",
}

func isBuiltinWantsDir(name string) bool {
	for _, p := range builtinUnitPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

func localCustomUnits() []localUnit {
	const dir = "/etc/systemd/system"
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var units []localUnit
	seen := map[string]bool{}
	collect := func(name, fullPath string) {
		if seen[name] || !strings.HasSuffix(name, ".service") && !strings.HasSuffix(name, ".timer") && !strings.HasSuffix(name, ".mount") {
			return
		}
		content, err := os.ReadFile(fullPath)
		if err != nil {
			return
		}
		u := localUnit{name: name, content: string(content)}
		u.enabled = unitEnabled(name)
		u.active = unitActive(name)
		units = append(units, u)
		seen[name] = true
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			if isBuiltinWantsDir(name+"/") || strings.HasSuffix(name, ".wants") || strings.HasSuffix(name, ".requires") {
				continue
			}
			// Non-standard subdirectory: look one level deep for units.
			sub, err := os.ReadDir(filepath.Join(dir, name))
			if err != nil {
				continue
			}
			for _, se := range sub {
				collect(se.Name(), filepath.Join(dir, name, se.Name()))
			}
			continue
		}
		collect(name, filepath.Join(dir, name))
	}
	sort.Slice(units, func(i, j int) bool { return units[i].name < units[j].name })
	return units
}

func systemctlIs(query, unit string) bool {
	out, err := runLocal("systemctl", query, unit)
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(out), "enabled") || strings.Contains(strings.ToLower(out), "active")
}

func unitEnabled(unit string) bool { return systemctlIs("is-enabled", unit) }
func unitActive(unit string) bool  { return systemctlIs("is-active", unit) }
