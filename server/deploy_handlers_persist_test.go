package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"shelley.exe.dev/db"
	"shelley.exe.dev/server/deploy"
)

// persistRun writes the finished run's row. Its SQL broke on 2026-11-04 when
// upstream removed db.Tx.Exec; the sync rebased cleanly over it, so the tree
// only failed to compile later. This test drives the real pipeline to a
// finished run and reads the persisted row back, so a broken write is caught
// here rather than by the nightly build.
func TestPersistRunWritesFinishedRow(t *testing.T) {
	// A stub exe.dev API that rejects the key: the run fails at validation and
	// finishes without creating any VM.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"bad key"}`, http.StatusForbidden)
	}))
	defer srv.Close()
	previous := deploy.SetExecEndpointForTest(srv.URL)
	defer deploy.SetExecEndpointForTest(previous)

	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	projectDir := t.TempDir()
	manager := deploy.NewManager(nil)
	run, err := manager.Start("exe.testkey", "demo", "", projectDir, 0, false, true, false, false)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := run.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if run.Status() != "failed" {
		t.Fatalf("run.Status() = %q, want failed (stub rejected the key)", run.Status())
	}

	s := &Server{db: database, logger: slog.Default()}
	s.persistRun(run)

	var vmName, status, errMsg, events string
	if err := database.Pool().Rx(context.Background(), func(ctx context.Context, rx *db.Rx) error {
		return rx.Conn().QueryRowContext(ctx,
			`SELECT vm_name, status, error, events FROM deploy_runs WHERE vm_name = ?`,
			"demo").Scan(&vmName, &status, &errMsg, &events)
	}); err != nil {
		t.Fatalf("reading persisted run: %v", err)
	}
	if vmName != "demo" {
		t.Errorf("vm_name = %q, want demo", vmName)
	}
	if status != "failed" {
		t.Errorf("status = %q, want failed", status)
	}
	if errMsg == "" {
		t.Error("error column is empty; the validation failure should be recorded")
	}
	var parsed []deploy.Event
	if err := json.Unmarshal([]byte(events), &parsed); err != nil {
		t.Fatalf("events column is not valid JSON: %v", err)
	}
	if len(parsed) == 0 {
		t.Error("events column is empty; the pipeline's log events should be recorded")
	}
}
