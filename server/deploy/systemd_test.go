package deploy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestValidateProjectDirRejectsFilesystemRoot(t *testing.T) {
	if _, err := validateProjectDir("/"); err == nil || !strings.Contains(err.Error(), "filesystem root") {
		t.Fatalf("want root-directory rejection, got %v", err)
	}
}

func TestStartRejectsImageCommandInjection(t *testing.T) {
	manager := NewManager(nil)
	_, err := manager.Start("token", "safe-name", "ubuntu:24.04\nrm victim", "/does/not/matter", 0, false, true, false, true)
	if err == nil || !strings.Contains(err.Error(), "image") {
		t.Fatalf("want image validation error, got %v", err)
	}
}

func TestProjectUnitsInDirSelectsOnlyAppAndCompanions(t *testing.T) {
	dir := t.TempDir()
	projectDir := "/home/exedev/playground/my-app"
	writeTestFile(t, filepath.Join(dir, "my-app.service"), "[Service]\nWorkingDirectory="+projectDir+"\nExecStart=./app\n")
	writeTestFile(t, filepath.Join(dir, "my-app.timer"), "[Timer]\nUnit=my-app.service\n")
	writeTestFile(t, filepath.Join(dir, "unrelated.service"), "[Service]\nExecStart=/usr/bin/unrelated\n")
	if err := os.Symlink("/dev/null", filepath.Join(dir, "masked.service")); err != nil {
		t.Fatal(err)
	}

	units, err := projectUnitsInDir(dir, projectDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := unitNames(units), []string{"my-app.service", "my-app.timer"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("units = %v, want %v", got, want)
	}
}

func TestDetectAppStartFastAPI(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "src", "mediahub", "app.py"), "from fastapi import FastAPI\napp = FastAPI()\n")
	report := &ProjectReport{Languages: []LangSpec{{Name: "python", Manager: "uv"}}}

	start, err := detectAppStart(dir, report, 8123)
	if err != nil {
		t.Fatal(err)
	}
	want := "uv run python -m uvicorn mediahub.app:app --host 0.0.0.0 --port 8123"
	if start.command != want {
		t.Fatalf("command = %q, want %q", start.command, want)
	}
	unit := generatedSystemdUnit("Media Hub", "/home/exedev/media-hub", start)
	if unit.name != "shelley-deploy-media-hub.service" || !unit.enabled || !unit.active {
		t.Fatalf("unexpected generated unit: %+v", unit)
	}
	for _, required := range []string{
		"User=exedev",
		"WorkingDirectory=/home/exedev/media-hub",
		"ExecStart=/bin/sh -lc 'exec " + want + "'",
		"Restart=on-failure",
	} {
		if !strings.Contains(unit.content, required) {
			t.Fatalf("generated unit missing %q:\n%s", required, unit.content)
		}
	}
}

func TestDetectAppStartRefusesUnknownProject(t *testing.T) {
	_, err := detectAppStart(t.TempDir(), &ProjectReport{}, 0)
	if err == nil || !strings.Contains(err.Error(), "select Skip systemd") {
		t.Fatalf("want actionable detection error, got %v", err)
	}
}

func TestReplacePortInUnit(t *testing.T) {
	input := "ExecStart=/app --port 8000 --listen=:8000\nEnvironment=PORT=8000\nVersion=18000\n"
	got, count := replacePortInUnit(input, 8000, 9000)
	want := "ExecStart=/app --port 9000 --listen=:9000\nEnvironment=PORT=9000\nVersion=18000\n"
	if got != want || count != 3 {
		t.Fatalf("replacePortInUnit = (%q, %d), want (%q, 3)", got, count, want)
	}
}

func TestFindVenvsFindsMultiple(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "api", ".venv", "pyvenv.cfg"), "home = /usr/bin\n")
	writeTestFile(t, filepath.Join(dir, "worker", "venv", "pyvenv.cfg"), "home = /usr/bin\n")
	got := findVenvs(dir)
	if len(got) != 2 {
		t.Fatalf("findVenvs = %v, want two virtualenvs", got)
	}
}

func TestDryRunGeneratesOnlyAppSystemdUnit(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "pyproject.toml"), "[project]\nname='demo'\n")
	writeTestFile(t, filepath.Join(dir, "uv.lock"), "version = 1\n")
	writeTestFile(t, filepath.Join(dir, "src", "demo", "app.py"), "from fastapi import FastAPI\napp = FastAPI()\n")

	withWhoamiEndpoint(t, func() {
		manager := NewManager(nil)
		run, err := manager.Start("token", "dry-run-systemd", "", dir, 8000, false, true, false, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := run.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
		if run.Status() != "success" {
			t.Fatalf("status = %s, error = %s", run.Status(), run.ErrMsg())
		}
		messages := eventMessages(run.SnapshotEvents())
		if !strings.Contains(messages, "Would generate, enable, and start shelley-deploy-") {
			t.Fatalf("dry-run did not describe generated app unit:\n%s", messages)
		}
		if strings.Contains(messages, "other custom unit") || strings.Contains(messages, "copying all") {
			t.Fatalf("dry-run contains unsafe broad systemd plan:\n%s", messages)
		}
	})
}

func TestDryRunSkipSystemdNeedsNoStartCommand(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "README.md"), "static project\n")

	withWhoamiEndpoint(t, func() {
		manager := NewManager(nil)
		run, err := manager.Start("token", "dry-run-skip", "", dir, 0, false, true, false, true)
		if err != nil {
			t.Fatal(err)
		}
		if err := run.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
		if run.Status() != "success" {
			t.Fatalf("status = %s, error = %s", run.Status(), run.ErrMsg())
		}
		messages := eventMessages(run.SnapshotEvents())
		if !strings.Contains(messages, "skip all systemd discovery") {
			t.Fatalf("missing skip-systemd plan:\n%s", messages)
		}
	})
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func withWhoamiEndpoint(t *testing.T, fn func()) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"email":"owner@example.com"}`))
	}))
	defer server.Close()
	previous := endpoint
	endpoint = server.URL
	defer func() { endpoint = previous }()
	fn()
}

func eventMessages(events []Event) string {
	var messages []string
	for _, event := range events {
		messages = append(messages, event.Message)
	}
	return strings.Join(messages, "\n")
}
