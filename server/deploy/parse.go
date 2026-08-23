package deploy

import (
	"bufio"
	"regexp"
	"strings"
)

// parseLines splits command output into a set of non-empty lines.
func parseLines(s string) map[string]bool {
	out := make(map[string]bool)
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" {
			out[line] = true
		}
	}
	return out
}

var npmListRe = regexp.MustCompile(`^[^ ]+@`)

// parseNpmList extracts package@version names from `npm ls -g --depth=0`
// output (skips the header lines and the top-level entry).
func parseNpmList(s string) map[string]bool {
	out := make(map[string]bool)
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		line = strings.TrimLeft(line, "\u251c\u2500\u2514\u2500\u00a0 ")
		line = strings.TrimSpace(line)
		if npmListRe.MatchString(line) {
			name := line
			// Strip the version: pkg@1.2.3, or @scope/pkg@1.2.3.
			if i := strings.LastIndex(name, "@"); i > 0 && name[:i] != "" {
				out[name[:i]] = true
			}
		}
	}
	return out
}

// freezeDiff compares `pip freeze` outputs and returns requirements missing on
// dst (pinned as in src).
func freezeDiff(srcFreeze, dstFreeze string) []string {
	src := parseRequirements(srcFreeze)
	dst := parseFreezeNames(dstFreeze)
	var missing []string
	for name, req := range src {
		if !dst[name] {
			missing = append(missing, req)
		}
	}
	return missing
}

func parseRequirements(freeze string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(freeze, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "-") {
			continue
		}
		name := normalizePipName(line)
		out[name] = line
	}
	return out
}

func parseFreezeNames(freeze string) map[string]bool {
	out := map[string]bool{}
	for _, line := range strings.Split(freeze, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "-") {
			continue
		}
		out[normalizePipName(line)] = true
	}
	return out
}

// normalizePipName extracts the canonical package name from a requirement
// line like "Flask==3.0.1" or "requests >= 2.0".
func normalizePipName(line string) string {
	s := line
	if i := strings.IndexAny(s, "=<>!~;[ "); i >= 0 {
		s = s[:i]
	}
	return strings.ToLower(strings.TrimSpace(s))
}

func findPasswdEntry(passwd, user string) string {
	for _, line := range strings.Split(passwd, "\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 && parts[0] == user {
			return line
		}
	}
	return ""
}

func parseUserNames(passwd string) map[string]bool {
	out := map[string]bool{}
	for _, line := range strings.Split(passwd, "\n") {
		if line == "" {
			continue
		}
		if name := strings.SplitN(line, ":", 2)[0]; name != "" {
			out[name] = true
		}
	}
	return out
}

func tail(s string) string {
	s = strings.TrimRight(s, "\n")
	lines := strings.Split(s, "\n")
	if len(lines) <= 8 {
		return s
	}
	return "... " + strings.Join(lines[len(lines)-8:], "\n")
}
