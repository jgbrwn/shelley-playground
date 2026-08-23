package deploy

import (
	"os"
	"runtime"
	"strings"
)

// SourceOSLabel returns "<os>/<arch>" of the machine Shelley runs on.
func SourceOSLabel() string { return runtime.GOOS + "/" + runtime.GOARCH }

// FullCloneSupported reports whether full src→dst state diffing makes sense:
// the source must be linux on amd64 (the exeuntu playground shape). macOS or
// arm64 sources can't meaningfully diff apt state against an Ubuntu VM.
func FullCloneSupported() bool {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return false
	}
	switch DetectSourceDistro() {
	case "ubuntu", "debian":
		return true
	}
	return false
}

// DetectSourceDistro reads /etc/os-release to identify debian-family sources.
// Used as a second gate for full clone (non-debian linux also can't apt-diff).
func DetectSourceDistro() string {
	id, ok := osReleaseField("ID")
	if !ok {
		return "unknown"
	}
	return id
}

func osReleaseField(field string) (string, bool) {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if k, v, found := strings.Cut(line, "="); found && k == field {
			return strings.Trim(v, `"`), true
		}
	}
	return "", false
}
