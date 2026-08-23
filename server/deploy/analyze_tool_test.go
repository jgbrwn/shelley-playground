package deploy

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestScanScriptTools(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "build.sh")
	os.WriteFile(script, []byte("#!/bin/bash\nffmpeg -i input.mp4 out.mp4\njq '.x' data.json\nsqlite3 db.sqlite 'select 1'\necho done\n"), 0o755)

	got := scanScriptTools(script)
	want := []string{"ffmpeg", "jq", "sqlite3"}
	if !equalStrings(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestScanScriptToolsEmpty(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "noop.sh")
	os.WriteFile(script, []byte("#!/bin/sh\necho hello\n"), 0o755)
	if got := scanScriptTools(script); len(got) != 0 {
		t.Fatalf("expected no tools, got %v", got)
	}
}

func TestIsScriptInterp(t *testing.T) {
	for _, ok := range []string{"bash", "sh", "python3", "python"} {
		if !isScriptInterp(ok) {
			t.Errorf("%q should be a script interp", ok)
		}
	}
	for _, bad := range []string{"node", "perl", "ruby", ""} {
		if isScriptInterp(bad) {
			t.Errorf("%q should NOT be a script interp", bad)
		}
	}
}

func TestAnalyzeProjectDetectsScriptTools(t *testing.T) {
	dir := t.TempDir()
	// Shell script that invokes known tools.
	os.WriteFile(filepath.Join(dir, "process.sh"), []byte("#!/usr/bin/env bash\nffmpeg -i in.mp4 out.mp4\njq '.x' < data\nsqlite3 db 'select 1'\n"), 0o755)
	// Python script that invokes make and gcc.
	os.WriteFile(filepath.Join(dir, "build.py"), []byte("#!/usr/bin/env python3\nimport subprocess\nsubprocess.run(['make', 'all'])\nsubprocess.run(['gcc', '-o', 'x', 'x.c'])\n"), 0o755)

	rep, err := AnalyzeProject(dir)
	if err != nil {
		t.Fatalf("AnalyzeProject: %v", err)
	}
	want := []string{"build-essential", "ffmpeg", "jq", "python3", "sqlite3"}
	got := rep.SystemPackages
	if !equalStrings(got, want) {
		t.Fatalf("system packages: got %v, want %v", got, want)
	}
}

func TestAnalyzeProjectMakefile(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Makefile"), []byte("all:\n\tgcc -o x x.c\n"), 0o644)
	rep, err := AnalyzeProject(dir)
	if err != nil {
		t.Fatalf("AnalyzeProject: %v", err)
	}
	if !containsStr(rep.SystemPackages, "build-essential") {
		t.Fatalf("Makefile should detect build-essential; got %v", rep.SystemPackages)
	}
}

func equalStrings(a, b []string) bool {
	a = append([]string(nil), a...)
	b = append([]string(nil), b...)
	sort.Strings(a)
	sort.Strings(b)
	return reflect.DeepEqual(a, b)
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func TestDepInstallPlan(t *testing.T) {
	rep := &ProjectReport{
		Languages: []LangSpec{
			{Name: "python", Manager: "uv"},
			{Name: "node", Manager: "npm"},
		},
	}
	got := depInstallPlan(rep)
	if got != "uv sync, npm install" {
		t.Fatalf("got %q, want %q", got, "uv sync, npm install")
	}

	// Empty report.
	if got := depInstallPlan(nil); got != "none detected" {
		t.Fatalf("got %q, want %q", got, "none detected")
	}
}
