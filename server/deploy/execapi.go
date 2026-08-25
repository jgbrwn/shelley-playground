package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// execClient talks to the exe.dev HTTPS API (POST https://exe.dev/exec with a
// bearer token; the body is an SSH-API command line like "new --name=x").
type execClient struct {
	token string
	hc    *http.Client
}

func newExecClient(token string) *execClient {
	return &execClient{token: token, hc: &http.Client{Timeout: 35 * time.Second}}
}

var endpoint = "https://exe.dev/exec"

// exec runs one CLI command via the HTTPS API and returns stdout. The API
// accepts exactly one command line in the request body; unlike SSH, it has no
// stdin stream.
func (c *execClient) exec(ctx context.Context, command string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(strings.TrimSpace(command)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	body := string(bodyBytes)
	switch resp.StatusCode {
	case http.StatusOK:
		return string(body), nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return "", fmt.Errorf("exe.dev rejected the API key (%d): %s", resp.StatusCode, firstLine(bodyBytes))
	case http.StatusUnprocessableEntity:
		// 422: the command ran but returned non-zero exit. The body has the
		// actual error output from the exe.dev CLI — surface it in full.
		return "", fmt.Errorf("exe.dev command failed (%d) for %q: %s", resp.StatusCode, firstWord(command), strings.TrimSpace(string(bodyBytes)))
	default:
		return "", fmt.Errorf("exe.dev API error %d for %q: %s", resp.StatusCode, firstWord(command), firstLine(bodyBytes))
	}
}

func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

func firstWord(s string) string {
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		return s[:i]
	}
	return s
}

func newVMCommand(vmName, image string) string {
	cmd := "new --name=" + vmName + " --json"
	if image != "" {
		cmd += " --image=" + image
	}
	return cmd
}

// SSHKey describes an account SSH public key returned by ssh-key list.
type SSHKey struct {
	PublicKey string `json:"public_key"`
}

// RegisterSSHKey ensures publicKey is registered with exe.dev. The private
// key never leaves this VM; exe.dev only receives its public counterpart.
func (c *execClient) RegisterSSHKey(ctx context.Context, publicKey string) (bool, error) {
	out, err := c.exec(ctx, "ssh-key list --json")
	if err != nil {
		return false, fmt.Errorf("listing registered SSH keys: %w", err)
	}
	var result struct {
		Keys []SSHKey `json:"ssh_keys"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		return false, fmt.Errorf("unexpected ssh-key list output: %w", err)
	}
	for _, key := range result.Keys {
		if key.PublicKey == publicKey {
			return false, nil
		}
	}
	if _, err := c.exec(ctx, "ssh-key add "+strconv.Quote(publicKey)); err != nil {
		return false, fmt.Errorf("registering deploy SSH key: %w", err)
	}
	return true, nil
}

// VM describes one VM from `ls --json`.
type VM struct {
	Name     string `json:"vm_name"`
	Status   string `json:"status"`
	HTTPSURL string `json:"https_url"`
	SSHHost  string `json:"ssh_host"`
}

func (v VM) Running() bool { return v.Status == "running" }

// Whoami validates the token and returns the account email.
func (c *execClient) Whoami(ctx context.Context) (string, error) {
	out, err := c.exec(ctx, "whoami")
	if err != nil {
		return "", err
	}
	var v struct {
		Email string `json:"email"`
	}
	if json.Unmarshal([]byte(out), &v) == nil && v.Email != "" {
		return v.Email, nil
	}
	return strings.TrimSpace(out), nil
}

// List returns all VMs on the account.
func (c *execClient) List(ctx context.Context) ([]VM, error) {
	out, err := c.exec(ctx, "ls --json")
	if err != nil {
		return nil, err
	}
	var res struct {
		VMs []VM `json:"vms"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		return nil, fmt.Errorf("unexpected ls output: %w", err)
	}
	return res.VMs, nil
}

// Find returns the named VM or nil if absent.
func (c *execClient) Find(ctx context.Context, name string) (*VM, error) {
	vms, err := c.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, vm := range vms {
		if vm.Name == name {
			v := vm
			return &v, nil
		}
	}
	return nil, nil
}

// DeleteVM removes a VM from the account (requires rm token permission).
func (c *execClient) DeleteVM(ctx context.Context, vm string) error {
	_, err := c.exec(ctx, "rm "+vm)
	return err
}

// NewExecClient constructs a client for one-off calls outside the pipeline.
func NewExecClient(token string) *execClient { return newExecClient(token) }

// SharePort points the VM's exe.dev proxy at the given app port.
func (c *execClient) SharePort(ctx context.Context, vm string, port int) error {
	_, err := c.exec(ctx, fmt.Sprintf("share port %s %d", vm, port))
	return err
}

// SetPublic toggles public access to the VM's proxy.
func (c *execClient) SetPublic(ctx context.Context, vm string, public bool) error {
	cmd := "share set-private " + vm
	if public {
		cmd = "share set-public " + vm
	}
	_, err := c.exec(ctx, cmd)
	return err
}

// NewVM creates a VM. image may be empty for the default image. The SSH
// bootstrap (installing our deploy key) happens in a follow-up API call once
// the VM exists — see sshkeys.go / pipeline.go.
func (c *execClient) NewVM(ctx context.Context, name, image string) error {
	cmd := "new --name=" + name + " --json"
	if image != "" {
		cmd += " --image=" + image
	}
	_, err := c.exec(ctx, cmd)
	return err
}
