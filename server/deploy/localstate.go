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

// localProjectUnits returns regular unit files in /etc/systemd/system that
// directly reference the project, plus regular companion units (for example a
// timer) that reference one of those project units. Symlinks are deliberately
// ignored: distro/package aliases in this directory commonly point back into
// /lib/systemd/system and are not user-authored app units.
type localUnit struct {
	name    string // e.g. "myapp.service"
	content string
	enabled bool
	active  bool
}

func localProjectUnits(projectDir string) ([]localUnit, error) {
	return projectUnitsInDir("/etc/systemd/system", projectDir, true)
}

func projectUnitsInDir(dir, projectDir string, queryState bool) ([]localUnit, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var candidates []localUnit
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !isSupportedUnitName(entry.Name()) {
			continue
		}
		content, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("reading systemd unit %s: %w", entry.Name(), err)
		}
		candidates = append(candidates, localUnit{name: entry.Name(), content: string(content)})
	}

	selected := map[string]bool{}
	for _, unit := range candidates {
		if strings.Contains(unit.content, projectDir) {
			selected[unit.name] = true
		}
	}
	// Include companion units that name a selected unit, iterating to support
	// small dependency chains without ever pulling in unrelated units.
	for changed := true; changed; {
		changed = false
		for _, unit := range candidates {
			if selected[unit.name] {
				continue
			}
			for name := range selected {
				if strings.Contains(unit.content, name) {
					selected[unit.name] = true
					changed = true
					break
				}
			}
		}
	}

	var units []localUnit
	for _, unit := range candidates {
		if !selected[unit.name] {
			continue
		}
		if queryState {
			unit.enabled = unitEnabled(unit.name)
			unit.active = unitActive(unit.name)
		}
		units = append(units, unit)
	}
	sort.Slice(units, func(i, j int) bool { return units[i].name < units[j].name })
	return units, nil
}

func isSupportedUnitName(name string) bool {
	for _, suffix := range []string{".service", ".timer", ".socket", ".path"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
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
