-- Tables for the "Deploy to new exe.dev VM" (forklift) feature.
-- New tables only; no edits to existing schema (keeps rebases clean).

-- Singleton row (id=1) holding the exe.dev HTTPS API bearer token used to
-- create deployment VMs. Never returned to clients in full.
CREATE TABLE IF NOT EXISTS deploy_settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    api_key TEXT NOT NULL,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- One row per deploy attempt. Events are a JSON array of structured log
-- entries streamed to the UI console via SSE while the run is active.
CREATE TABLE IF NOT EXISTS deploy_runs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    vm_name TEXT NOT NULL,
    image TEXT NOT NULL DEFAULT '',
    project_dir TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'running', -- running|success|failed
    error TEXT NOT NULL DEFAULT '',
    events TEXT NOT NULL DEFAULT '[]',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    finished_at DATETIME
);
