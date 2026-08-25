package deploy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestNewVMCommandHasNoSetupScript(t *testing.T) {
	cmd := newVMCommand("demo", "ubuntu:24.04")
	if got, want := cmd, "new --name=demo --json --image=ubuntu:24.04"; got != want {
		t.Fatalf("newVMCommand = %q, want %q", got, want)
	}
	if strings.Contains(cmd, "setup-script") || strings.Contains(cmd, "no-email") {
		t.Fatalf("unexpected create flags: %q", cmd)
	}
}

func TestRegisterSSHKeyAddsOnlyWhenMissing(t *testing.T) {
	publicKey := "ssh-ed25519 AAAA deploy"
	var commands []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		command := string(body)
		commands = append(commands, command)
		switch command {
		case "ssh-key list --json":
			_, _ = w.Write([]byte(`{"ssh_keys":[]}`))
		case "ssh-key add " + strconv.Quote(publicKey):
			_, _ = w.Write([]byte(`{"status":"added"}`))
		default:
			t.Fatalf("unexpected command %q", command)
		}
	}))
	defer srv.Close()

	previous := endpoint
	endpoint = srv.URL
	defer func() { endpoint = previous }()
	client := newExecClient("token")
	client.hc = srv.Client()

	added, err := client.RegisterSSHKey(context.Background(), publicKey)
	if err != nil {
		t.Fatal(err)
	}
	if !added {
		t.Fatal("expected key to be added")
	}
	if got, want := strings.Join(commands, "\n"), "ssh-key list --json\nssh-key add "+strconv.Quote(publicKey); got != want {
		t.Fatalf("commands = %q, want %q", got, want)
	}
}

func TestRegisterSSHKeySkipsExistingKey(t *testing.T) {
	publicKey := "ssh-ed25519 AAAA deploy"
	var commands []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		commands = append(commands, string(body))
		_, _ = w.Write([]byte(`{"ssh_keys":[{"public_key":"ssh-ed25519 AAAA deploy"}]}`))
	}))
	defer srv.Close()

	previous := endpoint
	endpoint = srv.URL
	defer func() { endpoint = previous }()
	client := newExecClient("token")
	client.hc = srv.Client()

	added, err := client.RegisterSSHKey(context.Background(), publicKey)
	if err != nil {
		t.Fatal(err)
	}
	if added || len(commands) != 1 || commands[0] != "ssh-key list --json" {
		t.Fatalf("existing key should not be added, commands = %v", commands)
	}
}

func TestExecClientWhoami(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "nope", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		w.Write([]byte(`{"email":"me@example.com"}`))
	}))
	defer srv.Close()

	c := newExecClient("tok")
	c.hc = srv.Client()
	// Point the client at the test server by overriding the endpoint.
	endpoint = srv.URL
	defer func() { endpoint = "https://exe.dev/exec" }()

	email, err := c.Whoami(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if email != "me@example.com" {
		t.Fatalf("got %q", email)
	}
}

func TestExecClientBadToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "invalid token", http.StatusUnauthorized)
	}))
	defer srv.Close()
	c := newExecClient("bad")
	c.hc = srv.Client()
	endpoint = srv.URL
	defer func() { endpoint = "https://exe.dev/exec" }()

	_, err := c.Whoami(context.Background())
	if err == nil || !strings.Contains(err.Error(), "API key") {
		t.Fatalf("want API key error, got %v", err)
	}
}

func TestFind(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"vms":[{"vm_name":"a","status":"running","https_url":"https://a.exe.xyz"},{"vm_name":"b","status":"stopped"}]}`))
	}))
	defer srv.Close()
	c := newExecClient("t")
	c.hc = srv.Client()
	endpoint = srv.URL
	defer func() { endpoint = "https://exe.dev/exec" }()

	vm, err := c.Find(context.Background(), "b")
	if err != nil {
		t.Fatal(err)
	}
	if vm == nil || vm.Name != "b" || vm.Running() {
		t.Fatalf("bad result: %+v %v", vm, err)
	}
	if vm2, _ := c.Find(context.Background(), "zz"); vm2 != nil {
		t.Fatal("expected nil")
	}
}
