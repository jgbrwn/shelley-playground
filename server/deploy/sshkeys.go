package deploy

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mikesmitty/edkey"
	"golang.org/x/crypto/ssh"
)

// sshKeyDir returns (and creates) the directory holding deploy SSH keys.
func sshKeyDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".config", "shelley", "deploy-ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// ensureSSHKey returns the account-wide deploy keypair, generating it on
// first use. Returns (privateKeyPath, publicKeyString, error).
func ensureSSHKey() (string, string, error) {
	dir, err := sshKeyDir()
	if err != nil {
		return "", "", err
	}
	privPath := filepath.Join(dir, "id_ed25519")
	pub, err := os.ReadFile(privPath + ".pub")
	if err == nil && len(pub) > 0 {
		return privPath, trimRight(string(pub)), nil
	}

	pubKey, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("generating key: %w", err)
	}
	pubSSH, err := ssh.NewPublicKey(pubKey)
	if err != nil {
		return "", "", err
	}
	pubStr := ssh.MarshalAuthorizedKey(pubSSH)

	privPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "OPENSSH PRIVATE KEY",
		Bytes: edkey.MarshalED25519PrivateKey(priv),
	})
	if err := os.WriteFile(privPath, privPEM, 0o600); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(privPath+".pub", []byte(pubStr), 0o644); err != nil {
		return "", "", err
	}
	return privPath, trimRight(string(pubStr)), nil
}

func trimRight(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r' || s[len(s)-1] == ' ') {
		s = s[:len(s)-1]
	}
	return s
}
