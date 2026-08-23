package deploy

import (
	"fmt"
	"strings"
)

// setupScript returns the first-boot script that installs our deploy SSH key
// for both plausible login users. Custom images may declare different
// exe.dev/login-user values, so cover exedev and root; whichever the platform
// uses, our key will be in its authorized_keys.
func setupScript(pubKey string) string {
	return fmt.Sprintf(`#!/bin/sh
set -e
KEY=%q
for HOME_DIR in /home/exedev /root; do
  if [ "$HOME_DIR" = "/home/exedev" ] && ! id exedev >/dev/null 2>&1 && [ ! -d /home/exedev ]; then
    # Image has no exedev user yet; create a matching one so paths align.
    useradd -m -s /bin/bash exedev || true
  fi
  if [ -d "$HOME_DIR/.ssh" ] || getent passwd "$HOME_DIR" >/dev/null 2>&1 || [ "$HOME_DIR" = "/root" ]; then
    mkdir -p "$HOME_DIR/.ssh"
    chmod 700 "$HOME_DIR/.ssh"
    grep -qxF "$KEY" "$HOME_DIR/.ssh/authorized_keys" 2>/dev/null || echo "$KEY" >> "$HOME_DIR/.ssh/authorized_keys"
    OWNER=$(stat -c '%%U' "$HOME_DIR")
    chown -R "$OWNER" "$HOME_DIR/.ssh"
    chmod 600 "$HOME_DIR/.ssh/authorized_keys"
  fi
done
`, pubKey)
}

// ensureExedevUser is run over an existing root SSH session when only root
// worked: it creates the exedev user (matching src uid/gid when given) so all
// later steps can use one consistent account.
func ensureExedevUser(uid, gid int, homeDir string) string {
	if uid <= 0 {
		return "useradd -m -s /bin/bash exedev"
	}
	if homeDir == "" {
		homeDir = "/home/exedev"
	}
	return fmt.Sprintf("id exedev >/dev/null 2>&1 || useradd -m -u %d -g %d -d %s -s /bin/bash exedev", uid, gid, homeDir)
}

func singleQuoted(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
