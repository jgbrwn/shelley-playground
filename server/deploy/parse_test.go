package deploy

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseLines(t *testing.T) {
	got := parseLines("a\n b \n\n c\nd")
	want := map[string]bool{"a": true, "b": true, "c": true, "d": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestSetDiff(t *testing.T) {
	src := map[string]bool{"curl": true, "jq": true, "vim": true}
	dst := map[string]bool{"curl": true}
	got := setDiff(src, dst)
	if !reflect.DeepEqual(got, []string{"jq", "vim"}) {
		t.Fatalf("got %v", got)
	}
}

func TestFreezeDiff(t *testing.T) {
	src := "flask==3.0.1\nrequests==2.31.0\n# comment\n-e git+https://x\n"
	dst := "Flask==3.0.1\nnumpy==1.26.0"
	got := freezeDiff(src, dst)
	if !reflect.DeepEqual(got, []string{"requests==2.31.0"}) {
		t.Fatalf("got %v", got)
	}
}

func TestNormalizePipName(t *testing.T) {
	cases := map[string]string{
		"Flask==3.0.1":       "flask",
		"requests >= 2.0":    "requests",
		"typing-extensions;": "typing-extensions",
		"pkg[extra]==1":      "pkg",
	}
	for in, want := range cases {
		if got := normalizePipName(in); got != want {
			t.Errorf("normalizePipName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseNpmList(t *testing.T) {
	out := "/usr/lib\n├── corepack@0.24.0\n├── npm@10.8.2\n└── typescript@5.5.4\n\ngot packages:"
	got := parseNpmList(out)
	if len(got) != 3 || !got["corepack"] || !got["typescript"] {
		t.Fatalf("got %v", got)
	}
}

func TestValidateVMName(t *testing.T) {
	for _, ok := range []string{"my-app", "web1", "a"} {
		if err := ValidateVMName(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "MyApp", "has space", "under_score", "-nope", "nope-", "two--hyphens"} {
		if err := ValidateVMName(bad); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}

func TestValidateAPIKey(t *testing.T) {
	for _, valid := range []string{"exe0.abc123", "exe1.ABC_xyz-123"} {
		if err := ValidateAPIKey(valid); err != nil {
			t.Errorf("%q: %v", valid, err)
		}
	}
	for _, invalid := range []string{"", "token", "exe1.bad token", "exe1.bad\nvalue"} {
		if err := ValidateAPIKey(invalid); err == nil {
			t.Errorf("%q: expected error", invalid)
		}
	}
}

func TestSetupScriptInstallsKey(t *testing.T) {
	s := setupScript("ssh-ed25519 AAAA test")
	for _, want := range []string{"/home/exedev", "root", "authorized_keys", "useradd -M", "id exedev", "usermod -s /bin/bash exedev"} {
		if !contains(s, want) {
			t.Errorf("setup script missing %q", want)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

func TestSetupScriptSyntax(t *testing.T) {
	path := filepath.Join(t.TempDir(), "setup.sh")
	if err := os.WriteFile(path, []byte(setupScript("ssh-ed25519 AAAA test")), 0o700); err != nil {
		t.Fatal(err)
	}
	if out, err := runLocal("sh", "-n", path); err != nil {
		t.Fatalf("setup script has invalid shell syntax: %v\n%s", err, out)
	}
}

func TestFindPasswdEntry(t *testing.T) {
	passwd := "root:x:0:0::/root:/bin/bash\nsvc:x:1001:1001::/opt/svc:/usr/sbin/nologin\n"
	if e := findPasswdEntry(passwd, "svc"); e == "" {
		t.Fatal("expected entry")
	}
	if e := findPasswdEntry(passwd, "nobody"); e != "" {
		t.Fatalf("expected empty, got %q", e)
	}
}
