package deploy

import "strings"

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
