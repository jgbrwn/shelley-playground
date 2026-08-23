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
	rootUser   = "root"
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

// connectWithUserLadder tries exedev then root, returning whichever works.
// The first successful username wins for all later operations.
func (r *Run) connectWithUserLadder(host, privPath string) (*sshTarget, error) {
	signer, err := parseSigner(privPath)
	if err != nil {
		return nil, fmt.Errorf("parsing deploy key: %w", err)
	}
	for _, user := range []string{exedevUser, rootUser} {
		t := &sshTarget{host: host, user: user, signer: signer}
		client, err := t.dial()
		if err != nil {
			r.emitf("info", "ssh", "SSH as %s failed: %v", user, err)
			continue
		}
		r.emitf("success", "ssh", "Connected as %s@%s", user, host)
		client.Close()
		return t, nil
	}
	return nil, fmt.Errorf("could not SSH to %s as %s or %s", host, exedevUser, rootUser)
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
