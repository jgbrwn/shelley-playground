package deploy

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// LangSpec describes one detected language/toolchain requirement.
type LangSpec struct {
	Name     string `json:"name"`     // "python", "node", "go", "rust", …
	Manager  string `json:"manager"`  // "uv", "pip", "npm", "yarn", "pnpm", …
	Manifest string `json:"manifest"` // relative path of the file it was detected from
	Version  string `json:"version,omitempty"`
}

// ProjectReport is the result of analyzing a project directory.
type ProjectReport struct {
	Dir            string     `json:"dir"`
	Languages      []LangSpec `json:"languages"`
	SystemPackages []string   `json:"system_packages"` // apt packages the dst will likely need
	Executables    []string   `json:"executables"`     // built/ELF binaries found (checked against dst at deploy time)
	Notes          []string   `json:"notes"`
}

// knownToolPackages maps commands an app may invoke to the apt package that
// provides them on Ubuntu/Debian.
var knownToolPackages = map[string]string{
	"python3": "python3", "pip3": "python3-pip", "uv": "uv",
	"node": "nodejs", "npm": "npm",
	"go":    "golang-go",
	"cargo": "cargo", "rustc": "rustc",
	"nginx": "nginx", "caddy": "caddy",
	"redis-server": "redis-server", "redis-cli": "redis-tools",
	"psql": "postgresql-client", "mysqld": "mysql-server", "sqlite3": "sqlite3",
	"ffmpeg": "ffmpeg", "convert": "imagemagick", "identify": "imagemagick",
	"curl": "curl", "wget": "wget",
	"git": "git", "rsync": "rsync", "jq": "jq",
	"tmux": "tmux", "htop": "htop", "vim": "vim",
	"docker": "docker.io", "make": "build-essential", "gcc": "build-essential",
	"g++": "build-essential", "pkg-config": "pkg-config",
	"unzip": "unzip", "zip": "zip", "tar": "tar",
	"ssh": "openssh-client", "scp": "openssh-client",
	"crontab": "cron",
}

// shebangMap extracts the interpreter command from a shebang line.
func shebangCommand(line string) string {
	line = strings.TrimPrefix(line, "#!")
	line = strings.TrimSpace(line)
	if line == "" {
		return ""
	}
	parts := strings.Fields(line)
	// Handle "#!/usr/bin/env python3"
	if filepath.Base(parts[0]) == "env" && len(parts) > 1 {
		return parts[1]
	}
	return filepath.Base(parts[0])
}

// AnalyzeProject scans dir for manifests, interpreters, and built binaries,
// producing the dependency report used by both the dry-run plan and the
// minimal-mode reconcile step.
func AnalyzeProject(dir string) (*ProjectReport, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", dir)
	}
	rep := &ProjectReport{Dir: dir}
	seenLang := map[string]bool{}
	addLang := func(l LangSpec) {
		if seenLang[l.Name+"|"+l.Manager] {
			return
		}
		seenLang[l.Name+"|"+l.Manager] = true
		rep.Languages = append(rep.Languages, l)
	}
	seenPkg := map[string]bool{}
	addPkg := func(p string) {
		if p == "" || seenPkg[p] {
			return
		}
		seenPkg[p] = true
		rep.SystemPackages = append(rep.SystemPackages, p)
	}

	// --- Manifest-based language detection ---
	exists := func(rel string) bool {
		_, err := os.Stat(filepath.Join(dir, rel))
		return err == nil
	}

	uvLock := exists("uv.lock")
	pyproject := exists("pyproject.toml")
	hasVenv := false
	for _, v := range []string{".venv", "venv"} {
		if isVenv(filepath.Join(dir, v)) {
			hasVenv = true
		}
	}
	if exists("requirements.txt") || pyproject || hasVenv || exists("Pipfile") || exists("setup.py") {
		mgr := "pip"
		if uvLock {
			mgr = "uv"
		}
		manifest := ""
		switch {
		case uvLock:
			manifest = "uv.lock"
		case pyproject:
			manifest = "pyproject.toml"
		case exists("requirements.txt"):
			manifest = "requirements.txt"
		case exists("Pipfile"):
			manifest = "Pipfile"
		case exists("setup.py"):
			manifest = "setup.py"
		}
		addLang(LangSpec{Name: "python", Manager: mgr, Manifest: manifest})
		addPkg("python3")
		addPkg("python3-pip")
		addPkg("python3-venv")
		if uvLock {
			addPkg("uv") // not in Ubuntu repos before 24.04; installer noted in report
		}
	}

	if exists("package.json") {
		mgr, manifest := "npm", "package.json"
		switch {
		case exists("pnpm-lock.yaml"):
			mgr, manifest = "pnpm", "pnpm-lock.yaml"
		case exists("yarn.lock"):
			mgr, manifest = "yarn", "yarn.lock"
		case exists("bun.lockb"):
			mgr, manifest = "bun", "bun.lockb"
		}
		addLang(LangSpec{Name: "node", Manager: mgr, Manifest: manifest})
		addPkg("nodejs")
		addPkg("npm")
	}

	// Makefile presence implies build-essential (make + gcc/cc toolchain).
	if exists("Makefile") || exists("makefile") || exists("GNUmakefile") {
		addPkg("build-essential")
	}

	if exists("go.mod") {
		addLang(LangSpec{Name: "go", Manager: "go", Manifest: "go.mod"})
		addPkg("golang-go")
	}
	if exists("Cargo.toml") {
		addLang(LangSpec{Name: "rust", Manager: "cargo", Manifest: "Cargo.toml"})
		addPkg("rustc")
		addPkg("cargo")
	}
	if exists("Gemfile") {
		addLang(LangSpec{Name: "ruby", Manager: "bundler", Manifest: "Gemfile"})
		addPkg("ruby-full")
		addPkg("bundler")
	}
	if exists("composer.json") {
		addLang(LangSpec{Name: "php", Manager: "composer", Manifest: "composer.json"})
		addPkg("php")
		addPkg("composer")
	}
	if exists("pom.xml") {
		addLang(LangSpec{Name: "java", Manager: "maven", Manifest: "pom.xml"})
		addPkg("default-jdk")
		addPkg("maven")
	}
	if exists("build.gradle") || exists("build.gradle.kts") {
		addLang(LangSpec{Name: "java", Manager: "gradle", Manifest: "build.gradle"})
		addPkg("default-jdk")
	}

	// --- Walk: shebangs, script-body tool scans, executables ---
	shebangSeen := map[string]bool{}
	scriptToolSeen := map[string]bool{}
	walkLimit := 20000
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			switch name {
			case ".git", "node_modules", "__pycache__", ".venv", "venv", "target", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		walkLimit--
		if walkLimit <= 0 {
			return filepath.SkipAll
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		// Executables: record ELF binaries for runtime ldd checks on dst.
		if info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			if isELF(filepath.Join(dir, relPath(dir, path))) {
				rep.Executables = append(rep.Executables, relPath(dir, path))
				sort.Strings(rep.Executables)
				if len(rep.Executables) > 20 {
					rep.Executables = rep.Executables[:20]
				}
			}
		}
		// Shebangs + script-body tool scan: for small text files, peek at
		// the first line for a shebang. If it's a shell or python script,
		// also scan the body for invocations of known tools (ffmpeg, jq,
		// sqlite3, etc.) so minimal-mode dep detection catches them.
		if info.Mode().IsRegular() && info.Size() < 512*1024 {
			if cmd := firstLineShebang(path); cmd != "" {
				shebangSeen[cmd] = true
				if isScriptInterp(cmd) {
					for _, tool := range scanScriptTools(path) {
						scriptToolSeen[tool] = true
					}
				}
			}
		}
		return nil
	})

	for cmd := range shebangSeen {
		addPkg(knownToolPackages[cmd])
	}
	for tool := range scriptToolSeen {
		addPkg(knownToolPackages[tool])
	}
	for _, exe := range rep.Executables {
		base := strings.ToLower(filepath.Base(exe))
		addPkg(knownToolPackages[base])
	}
	sort.Strings(rep.SystemPackages)
	return rep, nil
}

func relPath(root, path string) string {
	r, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return r
}

func firstLineShebang(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, 256)
	n, _ := f.Read(buf)
	if n < 2 || buf[0] != '#' || buf[1] != '!' {
		return ""
	}
	return shebangCommand(string(buf[:n]))
}

func isELF(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 4)
	n, _ := f.Read(buf)
	return n == 4 && buf[0] == 0x7f && string(buf[1:]) == "ELF"
}

// isScriptInterp reports whether a shebang interpreter means the file is a
// script whose body may invoke other tools (shells and python).
func isScriptInterp(cmd string) bool {
	switch cmd {
	case "bash", "sh", "dash", "zsh", "ksh", "python", "python3":
		return true
	}
	return false
}

// toolWordRe matches bare command invocations of known tools at the start of
// a word boundary: `ffmpeg ...`, `$(ffmpeg ...)`, `| jq`, `ffmpeg\n`, etc.
// It intentionally avoids matching inside paths, URLs, or variable names.
var toolWordRe = regexp.MustCompile(`(?:^|[^\w/-])(ffmpeg|sqlite3|jq|redis-cli|redis-server|psql|mysqld|convert|identify|curl|wget|git|rsync|tmux|htop|vim|docker|make|gcc|g\+\+|pkg-config|unzip|zip|nginx|caddy|cargo|rustc|go|node|npm|pip3)\b`)

// scanScriptTools reads a script file and returns the set of known tool
// commands it invokes. Only the first 64KB is scanned; that's enough for
// script files while keeping the walk fast.
func scanScriptTools(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	buf := make([]byte, 64*1024)
	n, _ := io.ReadFull(f, buf)
	if n <= 0 {
		return nil
	}
	body := string(buf[:n])
	seen := map[string]bool{}
	for _, m := range toolWordRe.FindAllStringSubmatch(body, -1) {
		seen[m[1]] = true
	}
	var out []string
	for cmd := range seen {
		out = append(out, cmd)
	}
	sort.Strings(out)
	return out
}

// localCommandExists reports whether a command is on PATH locally.
func localCommandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// BuildMarkdownReport renders the analysis as copy-pastable markdown.
func BuildMarkdownReport(rep *ProjectReport) string {
	var b strings.Builder
	b.WriteString("# Dependency report\n\n")
	fmt.Fprintf(&b, "Project: `%s`\n\n", rep.Dir)

	b.WriteString("## Languages & managers\n\n")
	if len(rep.Languages) == 0 {
		b.WriteString("_None detected._\n")
	} else {
		b.WriteString("| Language | Manager | Detected from |\n|---|---|---|\n")
		for _, l := range rep.Languages {
			v := l.Version
			if v == "" {
				v = "—"
			}
			fmt.Fprintf(&b, "| %s | %s | `%s` (%s) |\n", l.Name, l.Manager, l.Manifest, v)
		}
	}

	b.WriteString("\n## System packages (apt) needed on destination\n\n")
	if len(rep.SystemPackages) == 0 {
		b.WriteString("_None detected._\n")
	} else {
		b.WriteString("```bash\nsudo apt-get install -y \\\n  " + strings.Join(rep.SystemPackages, " \\\n  ") + "\n```\n")
	}

	if len(rep.Executables) > 0 {
		b.WriteString("\n## Built executables\n\nShared-library requirements of these binaries will be checked (and installed via ldd diffing) on destination:\n\n")
		for _, e := range rep.Executables {
			fmt.Fprintf(&b, "- `%s`\n", e)
		}
	}

	if len(rep.Notes) > 0 {
		b.WriteString("\n## Notes\n\n")
		for _, n := range rep.Notes {
			fmt.Fprintf(&b, "- %s\n", n)
		}
	}
	return b.String()
}
