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

func TestNewVMCommandHasDeployTag(t *testing.T) {
	cmd := newVMCommand("demo", "ubuntu:24.04")
	want := "new --name=demo --json --tag=shelley-deploy --image=ubuntu:24.04"
	if cmd != want {
		t.Fatalf("newVMCommand = %q, want %q", cmd, want)
	}
	if strings.Contains(cmd, "setup-script") || strings.Contains(cmd, "no-email") {
		t.Fatalf("unexpected create flags: %q", cmd)
	}
}

func TestRegisterSSHKeyForTag(t *testing.T) {
	publicKey := "ssh-ed25519 AAAA deploy"
	var command string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		command = string(body)
		_, _ = w.Write([]byte(`{"status":"added"}`))
	}))
	defer srv.Close()

	previous := endpoint
	endpoint = srv.URL
	defer func() { endpoint = previous }()
	client := newExecClient("token")
	client.hc = srv.Client()

	if err := client.RegisterSSHKeyForTag(context.Background(), publicKey); err != nil {
		t.Fatal(err)
	}
	want := "ssh-key add --tag=shelley-deploy " + strconv.Quote(publicKey)
	if command != want {
		t.Fatalf("command = %q, want %q", command, want)
	}
}

func TestRegisterSSHKeyForTagAlreadyExists(t *testing.T) {
	publicKey := "ssh-ed25519 AAAA deploy"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `this SSH key is already associated with your account`, http.StatusUnprocessableEntity)
	}))
	defer srv.Close()

	previous := endpoint
	endpoint = srv.URL
	defer func() { endpoint = previous }()
	client := newExecClient("token")
	client.hc = srv.Client()

	if err := client.RegisterSSHKeyForTag(context.Background(), publicKey); err != nil {
		t.Fatalf("already-associated should be treated as success, got %v", err)
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
