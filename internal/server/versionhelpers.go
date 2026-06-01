package server

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
)

func goOS() string   { return runtime.GOOS }
func goArch() string { return runtime.GOARCH }

func splitFeatures(s string) []string {
	return strings.Split(s, ",")
}

func detectRocmVersion() string {
	if data, err := os.ReadFile("/opt/rocm/version.txt"); err == nil {
		return strings.TrimSpace(string(data))
	}
	cmd := exec.Command("rocminfo", "--version")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}
	for _, field := range strings.Fields(strings.TrimSpace(string(out))) {
		if strings.HasPrefix(field, "v") {
			return field
		}
	}
	return ""
}
