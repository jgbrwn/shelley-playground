package deploy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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
