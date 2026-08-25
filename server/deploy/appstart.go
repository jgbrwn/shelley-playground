package deploy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type appStart struct {
	command string
	source  string
}

// detectAppStart finds a deterministic command suitable for a generated
// systemd service. It intentionally errors instead of guessing when no known
// entry point exists.
func detectAppStart(projectDir string, report *ProjectReport, port int) (appStart, error) {
	if command := procfileWebCommand(filepath.Join(projectDir, "Procfile")); command != "" {
		return appStart{command: loopbackCommand(command), source: "Procfile web process"}, nil
	}

	for _, name := range []string{"start.sh", "run.sh", "serve.sh"} {
		path := filepath.Join(projectDir, name)
		info, err := os.Stat(path)
		if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return appStart{command: loopbackCommand("./" + name), source: name}, nil
		}
	}

	if manager, ok := packageStartManager(filepath.Join(projectDir, "package.json"), report); ok {
		return appStart{command: loopbackCommand(manager + " run start"), source: "package.json start script"}, nil
	}

	if module, variable := findPythonWebApp(projectDir, "FastAPI"); module != "" {
		command := pythonRunner(report) + " -m uvicorn " + module + ":" + variable + " --host 127.0.0.1"
		if port != 0 {
			command += fmt.Sprintf(" --port %d", port)
		}
		return appStart{command: command, source: "FastAPI application"}, nil
	}
	if module, variable := findPythonWebApp(projectDir, "Flask"); module != "" {
		command := pythonRunner(report) + " -m flask --app " + module + ":" + variable + " run --host 127.0.0.1"
		if port != 0 {
			command += fmt.Sprintf(" --port %d", port)
		}
		return appStart{command: command, source: "Flask application"}, nil
	}
	if fileExists(filepath.Join(projectDir, "manage.py")) {
		appPort := port
		if appPort == 0 {
			appPort = 8000
		}
		return appStart{
			command: fmt.Sprintf("%s manage.py runserver 127.0.0.1:%d", pythonRunner(report), appPort),
			source:  "Django manage.py",
		}, nil
	}
	if fileExists(filepath.Join(projectDir, "go.mod")) {
		return appStart{command: "go run .", source: "go.mod"}, nil
	}
	if fileExists(filepath.Join(projectDir, "Cargo.toml")) {
		return appStart{command: "cargo run --release", source: "Cargo.toml"}, nil
	}
	if fileExists(filepath.Join(projectDir, "public/index.php")) {
		appPort := port
		if appPort == 0 {
			appPort = 8000
		}
		return appStart{
			command: fmt.Sprintf("php -S 127.0.0.1:%d -t public", appPort),
			source:  "public/index.php",
		}, nil
	}

	return appStart{}, fmt.Errorf("no project systemd unit or supported app start command found; add a Procfile web entry, package.json start script, executable start.sh/run.sh/serve.sh, or select Skip systemd")
}

func loopbackCommand(command string) string {
	for _, pattern := range []struct {
		from string
		to   string
	}{
		{"HOST=0.0.0.0", "HOST=127.0.0.1"},
		{"BIND_HOST=0.0.0.0", "BIND_HOST=127.0.0.1"},
		{"FLASK_RUN_HOST=0.0.0.0", "FLASK_RUN_HOST=127.0.0.1"},
		{"UVICORN_HOST=0.0.0.0", "UVICORN_HOST=127.0.0.1"},
		{"HOST=::", "HOST=127.0.0.1"},
		{"--host ::", "--host 127.0.0.1"},
		{"--host=::", "--host=127.0.0.1"},
		{"--bind ::", "--bind 127.0.0.1"},
		{"--bind=::", "--bind=127.0.0.1"},
		{"--host 0.0.0.0", "--host 127.0.0.1"},
		{"--host=0.0.0.0", "--host=127.0.0.1"},
		{"--bind 0.0.0.0", "--bind 127.0.0.1"},
		{"--bind=0.0.0.0", "--bind=127.0.0.1"},
		{"0.0.0.0:", "127.0.0.1:"},
		{"[::]:", "127.0.0.1:"},
	} {
		command = strings.ReplaceAll(command, pattern.from, pattern.to)
	}
	return command
}

func procfileWebCommand(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if ok && strings.TrimSpace(key) == "web" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func packageStartManager(path string, report *ProjectReport) (string, bool) {
	if !packageHasScript(path, "start") {
		return "", false
	}
	for _, lang := range report.Languages {
		if lang.Name != "node" {
			continue
		}
		switch lang.Manager {
		case "pnpm", "yarn", "bun":
			return lang.Manager, true
		default:
			return "npm", true
		}
	}
	return "npm", true
}

func packageHasScript(path, name string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var manifest struct {
		Scripts map[string]string `json:"scripts"`
	}
	return json.Unmarshal(data, &manifest) == nil && strings.TrimSpace(manifest.Scripts[name]) != ""
}

func pythonRunner(report *ProjectReport) string {
	if langNeeds(report, "python", "uv") {
		return "uv run python"
	}
	return ".venv/bin/python"
}

func findPythonWebApp(projectDir, constructor string) (string, string) {
	assignment := regexp.MustCompile(`(?m)^([A-Za-z_][A-Za-z0-9_]*)\s*=\s*` + regexp.QuoteMeta(constructor) + `\s*\(`)
	var module, variable string
	_ = filepath.WalkDir(projectDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || module != "" {
			return nil
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".venv", "venv", "node_modules", "__pycache__", "vendor", "target":
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(entry.Name()) != ".py" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil || len(data) > 512*1024 {
			return nil
		}
		match := assignment.FindSubmatch(data)
		if match == nil {
			return nil
		}
		rel, err := filepath.Rel(projectDir, path)
		if err != nil {
			return nil
		}
		rel = strings.TrimPrefix(filepath.ToSlash(rel), "src/")
		rel = strings.TrimSuffix(rel, ".py")
		rel = strings.TrimSuffix(rel, "/__init__")
		module = strings.ReplaceAll(rel, "/", ".")
		variable = string(match[1])
		return nil
	})
	return module, variable
}

func generatedSystemdUnit(projectName, projectDir string, start appStart) localUnit {
	name := "shelley-deploy-" + systemdName(projectName) + ".service"
	command := strings.ReplaceAll("exec "+start.command, "%", "%%")
	content := fmt.Sprintf(`[Unit]
Description=Shelley deployed app: %s
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=exedev
WorkingDirectory=%s
Environment=HOME=/home/exedev
Environment=HOST=127.0.0.1
Environment=FLASK_RUN_HOST=127.0.0.1
Environment=UVICORN_HOST=127.0.0.1
Environment=PATH=/home/exedev/.local/bin:/home/exedev/.bun/bin:/usr/local/bin:/usr/bin:/bin
ExecStart=/bin/sh -lc %s
Restart=on-failure
RestartSec=3

[Install]
WantedBy=multi-user.target
`, projectName, projectDir, singleQuoted(command))
	return localUnit{name: name, content: content, enabled: true, active: true}
}

var systemdNameRE = regexp.MustCompile(`[^a-z0-9-]+`)

func systemdName(name string) string {
	name = strings.ToLower(name)
	name = systemdNameRE.ReplaceAllString(name, "-")
	name = strings.Trim(name, "-")
	if name == "" {
		return "app"
	}
	return name
}
