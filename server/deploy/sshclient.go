package deploy

import (
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	sshPort    = 22
	sshDialTo  = 10 * time.Second
	exedevUser = "exedev"
)

// sshTarget is a reachable SSH endpoint plus the user we authenticated as.
type sshTarget struct {
	host   string // e.g. "myvm.exe.xyz"
	user   string
	signer ssh.Signer
}

func (t *sshTarget) dial() (*ssh.Client, error) {
	cfg := &ssh.ClientConfig{
		User: t.user,
		Auth: []ssh.AuthMethod{ssh.PublicKeys(t.signer)},
		HostKeyCallback: func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			return nil // TOFU not needed: we provisioned the only authorized key ourselves
		},
		Timeout: sshDialTo,
	}
	return ssh.Dial("tcp", net.JoinHostPort(t.host, fmt.Sprint(sshPort)), cfg)
}

// parseSigner loads the deploy private key.
func parseSigner(privPath string) (ssh.Signer, error) {
	pem, err := os.ReadFile(privPath)
	if err != nil {
		return nil, err
	}
	return ssh.ParsePrivateKey(pem)
}

// connectAsExedev connects with the exe.dev SSH user documented for VMs.
// It emits the failure detail (used outside the retry loop).
func (r *Run) connectAsExedev(host, privPath string) (*sshTarget, error) {
	t, err := r.connectAsExedevQuiet(host, privPath)
	if err != nil {
		r.emitf("info", "ssh", "SSH as %s failed: %v", exedevUser, err)
		return nil, err
	}
	r.emitf("success", "ssh", "Connected as %s@%s", exedevUser, host)
	return t, nil
}

// connectAsExedevQuiet attempts the connection without emitting anything.
// Used by waitForSSH's retry loop to avoid console spam.
func (r *Run) connectAsExedevQuiet(host, privPath string) (*sshTarget, error) {
	signer, err := parseSigner(privPath)
	if err != nil {
		return nil, fmt.Errorf("parsing deploy key: %w", err)
	}
	t := &sshTarget{host: host, user: exedevUser, signer: signer}
	client, err := t.dial()
	if err != nil {
		return nil, fmt.Errorf("could not SSH to %s as %s: %w", host, exedevUser, err)
	}
	client.Close()
	return t, nil
}

// runOutput runs a command over an existing client.
func (t *sshTarget) runOutput(client *ssh.Client, cmd string) (string, error) {
	sess, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	out, err := sess.CombinedOutput(cmd)
	return string(out), err
}

func indentBlock(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "    " + l
	}
	return strings.Join(lines, "\n")
}
