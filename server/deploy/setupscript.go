package deploy

import (
	"fmt"
	"strings"
)

// setupScript returns an idempotent first-boot script that ensures the exedev
// account/home exist and installs the deploy key for exedev and root. Existing
// accounts, homes, .ssh directories, and authorized_keys files are preserved.
func setupScript(pubKey string) string {
	return fmt.Sprintf(`#!/bin/sh
set -eu
KEY=%q

if ! id exedev >/dev/null 2>&1; then
  if [ -d /home/exedev ]; then
    useradd -M -d /home/exedev -s /bin/bash exedev
  else
    useradd -m -d /home/exedev -s /bin/bash exedev
  fi
fi

for USER_NAME in exedev root; do
  ENTRY=$(getent passwd "$USER_NAME")
  HOME_DIR=$(printf '%%s' "$ENTRY" | cut -d: -f6)
  GROUP_NAME=$(id -gn "$USER_NAME")
  install -d -m 700 -o "$USER_NAME" -g "$GROUP_NAME" "$HOME_DIR/.ssh"
  touch "$HOME_DIR/.ssh/authorized_keys"
  grep -qxF "$KEY" "$HOME_DIR/.ssh/authorized_keys" || printf '%%s\n' "$KEY" >> "$HOME_DIR/.ssh/authorized_keys"
  chown "$USER_NAME:$GROUP_NAME" "$HOME_DIR/.ssh/authorized_keys"
  chmod 600 "$HOME_DIR/.ssh/authorized_keys"
done
`, pubKey)
}

// ensureExedevUser is run over an existing root SSH session when only root
// worked: it creates the exedev user (matching src uid/gid when given) so all
// later steps can use one consistent account.
func ensureExedevUser(uid, gid int, homeDir string) string {
	if uid <= 0 {
		return "id exedev >/dev/null 2>&1 || useradd -m -d /home/exedev -s /bin/bash exedev"
	}
	if homeDir == "" {
		homeDir = "/home/exedev"
	}
	return fmt.Sprintf("id exedev >/dev/null 2>&1 || useradd -m -u %d -g %d -d %s -s /bin/bash exedev", uid, gid, homeDir)
}

func singleQuoted(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func quoteShellArgs(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = singleQuoted(arg)
	}
	return strings.Join(quoted, " ")
}
