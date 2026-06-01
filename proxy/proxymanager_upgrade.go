package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// ---------------------------------------------------------------------------
// Request / response types
// ---------------------------------------------------------------------------

type upgradeRequest struct {
	Method string `json:"method"` // "prebuilt" or "source"
	Ref    string `json:"ref"`    // build tag like "b5142"
}

// upgradeProgressEvent is streamed as newline-delimited JSON during the upgrade.
type upgradeProgressEvent struct {
	Event string `json:"event"`
	Msg   string `json:"message,omitempty"`
}

// ---------------------------------------------------------------------------
// API handler
// ---------------------------------------------------------------------------

func (pm *ProxyManager) apiUpgrade(c *gin.Context) {
	var req upgradeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.Method == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "method is required"})
		return
	}
	if req.Method != "prebuilt" && req.Method != "source" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "method must be 'prebuilt' or 'source'"})
		return
	}
	if req.Ref == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ref is required"})
		return
	}

	// Set SSE headers for streaming progress.
	c.Header("Content-Type", "application/x-ndjson")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("X-Accel-Buffering", "no")

	// Determine the upgrade method handler.
	var err error
	switch req.Method {
	case "prebuilt":
		err = pm.upgradePrebuilt(c, req.Ref)
	case "source":
		err = pm.upgradeFromSource(c, req.Ref)
	default:
		err = fmt.Errorf("unsupported upgrade method: %s", req.Method)
	}

	if err != nil {
		// Emit a failure event.
		pm.sendUpgradeEvent(c, "error", fmt.Sprintf("upgrade failed: %v", err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	pm.sendUpgradeEvent(c, "complete", fmt.Sprintf("upgrade to %s complete", req.Ref))
	c.JSON(http.StatusOK, gin.H{
		"status": "complete",
		"ref":    req.Ref,
		"method": req.Method,
	})
}

// sendUpgradeEvent writes a progress event to the response stream.
func (pm *ProxyManager) sendUpgradeEvent(c *gin.Context, event, msg string) {
	evt := upgradeProgressEvent{Event: event, Msg: msg}
	b, _ := json.Marshal(evt)
	if _, err := c.Writer.Write(b); err == nil {
		c.Writer.Write([]byte("\n"))
		c.Writer.Flush()
	}
}

// ---------------------------------------------------------------------------
// prebuilt upgrade — download a pre-built binary
// ---------------------------------------------------------------------------

func (pm *ProxyManager) upgradePrebuilt(c *gin.Context, ref string) error {
	serverPath, err := pm.currentServerPath()
	if err != nil {
		return fmt.Errorf("determine server path: %w", err)
	}

	// Backup current binary.
	backupPath := serverPath + ".bak"
	if _, err := os.Stat(serverPath); err == nil {
		if copyErr := copyFile(serverPath, backupPath); copyErr != nil {
			return fmt.Errorf("backup current binary: %w", copyErr)
		}
	}

	pm.sendUpgradeEvent(c, "downloading", fmt.Sprintf("fetching pre-built binary for %s", ref))

	// Download from HuggingFace llama.cpp release.
	downloadURL := fmt.Sprintf("https://github.com/ggerganov/llama.cpp/releases/download/%s/llama-server", ref)

	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, downloadURL, nil)
	if err != nil {
		return fmt.Errorf("create download request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		pm.sendUpgradeEvent(c, "rollback", "downloading failed — restoring backup")
		pm.restoreBackup(backupPath, serverPath)
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		pm.sendUpgradeEvent(c, "rollback", fmt.Sprintf("HTTP %d — restoring backup", resp.StatusCode))
		pm.restoreBackup(backupPath, serverPath)
		return fmt.Errorf("download returned HTTP %d", resp.StatusCode)
	}

	// Write to a temp file, then atomically replace.
	tmpFile, err := os.CreateTemp(filepath.Dir(serverPath), "llama-server-upgrade-*")
	if err != nil {
		pm.sendUpgradeEvent(c, "rollback", "creating temp file failed — restoring backup")
		pm.restoreBackup(backupPath, serverPath)
		return fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath) // clean up on error path

	pm.sendUpgradeEvent(c, "writing", "writing downloaded binary")

	if _, err := io.Copy(tmpFile, resp.Body); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		pm.sendUpgradeEvent(c, "rollback", "writing binary failed — restoring backup")
		pm.restoreBackup(backupPath, serverPath)
		return fmt.Errorf("write binary: %w", err)
	}
	tmpFile.Close()

	// Make executable.
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		os.Remove(tmpPath)
		pm.sendUpgradeEvent(c, "rollback", "chmod failed — restoring backup")
		pm.restoreBackup(backupPath, serverPath)
		return fmt.Errorf("chmod: %w", err)
	}

	// Atomically replace the binary.
	pm.sendUpgradeEvent(c, "replacing", "replacing current binary")
	if err := os.Rename(tmpPath, serverPath); err != nil {
		os.Remove(tmpPath)
		pm.sendUpgradeEvent(c, "rollback", "rename failed — restoring backup")
		pm.restoreBackup(backupPath, serverPath)
		return fmt.Errorf("rename: %w", err)
	}

	pm.sendUpgradeEvent(c, "smoke-check", "running post-upgrade smoke test")
	if err := pm.smokeTest(serverPath); err != nil {
		pm.sendUpgradeEvent(c, "rollback", fmt.Sprintf("smoke test failed — restoring backup: %v", err))
		pm.restoreBackup(backupPath, serverPath)
		return fmt.Errorf("smoke test failed: %w", err)
	}

	pm.sendUpgradeEvent(c, "restart", "restarting llama-server process")
	if err := pm.restartLlamaServer(); err != nil {
		pm.sendUpgradeEvent(c, "rollback", fmt.Sprintf("restart failed — restoring backup: %v", err))
		pm.restoreBackup(backupPath, serverPath)
		return fmt.Errorf("restart: %w", err)
	}

	pm.sendUpgradeEvent(c, "complete", "upgrade complete — llama-server restarted")
	return nil
}

// ---------------------------------------------------------------------------
// source upgrade — build from source
// ---------------------------------------------------------------------------

func (pm *ProxyManager) upgradeFromSource(c *gin.Context, ref string) error {
	serverPath, err := pm.currentServerPath()
	if err != nil {
		return fmt.Errorf("determine server path: %w", err)
	}

	// Backup current binary.
	backupPath := serverPath + ".bak"
	if _, err := os.Stat(serverPath); err == nil {
		if copyErr := copyFile(serverPath, backupPath); copyErr != nil {
			return fmt.Errorf("backup current binary: %w", copyErr)
		}
	}

	// Use the models dir as a workspace for source checkout.
	modelsDir := pm.modelsDir()
	if modelsDir == "" {
		return fmt.Errorf("models directory unknown; set modelsDir in config")
	}
	workspace := filepath.Join(modelsDir, ".upgrade-src")

	pm.sendUpgradeEvent(c, "checkout", fmt.Sprintf("checking out ref %s in %s", ref, workspace))

	// Clone or update the repo.
	if _, err := os.Stat(filepath.Join(workspace, ".git")); os.IsNotExist(err) {
		pm.sendUpgradeEvent(c, "cloning", "initial clone of llama.cpp")
		cmd := exec.CommandContext(c.Request.Context(), "git", "clone", "https://github.com/ggerganov/llama.cpp", workspace)
		if out, err := cmd.CombinedOutput(); err != nil {
			os.RemoveAll(workspace)
			pm.sendUpgradeEvent(c, "rollback", "git clone failed — restoring backup")
			pm.restoreBackup(backupPath, serverPath)
			return fmt.Errorf("git clone: %w\n%s", err, string(out))
		}
	} else {
		pm.sendUpgradeEvent(c, "fetching", "fetching latest refs")
		cmd := exec.CommandContext(c.Request.Context(), "git", "-C", workspace, "fetch", "origin")
		if out, err := cmd.CombinedOutput(); err != nil {
			os.RemoveAll(workspace)
			pm.sendUpgradeEvent(c, "rollback", "git fetch failed — restoring backup")
			pm.restoreBackup(backupPath, serverPath)
			return fmt.Errorf("git fetch: %w\n%s", err, string(out))
		}
	}

	pm.sendUpgradeEvent(c, "checkout-ref", fmt.Sprintf("checking out %s", ref))
	cmd := exec.CommandContext(c.Request.Context(), "git", "-C", workspace, "checkout", ref)
	if out, err := cmd.CombinedOutput(); err != nil {
		os.RemoveAll(workspace)
		pm.sendUpgradeEvent(c, "rollback", "git checkout failed — restoring backup")
		pm.restoreBackup(backupPath, serverPath)
		return fmt.Errorf("git checkout: %w\n%s", err, string(out))
	}

	pm.sendUpgradeEvent(c, "building", "compiling llama-server")
	cmd = exec.CommandContext(c.Request.Context(), "make", "-C", workspace, "llama-server")
	if out, err := cmd.CombinedOutput(); err != nil {
		os.RemoveAll(workspace)
		pm.sendUpgradeEvent(c, "rollback", fmt.Sprintf("build failed — restoring backup:\n%s", string(out)))
		pm.restoreBackup(backupPath, serverPath)
		return fmt.Errorf("make: %w\n%s", err, string(out))
	}

	newServer := filepath.Join(workspace, "bin", "llama-server")
	if _, err := os.Stat(newServer); os.IsNotExist(err) {
		os.RemoveAll(workspace)
		pm.sendUpgradeEvent(c, "rollback", "built binary not found — restoring backup")
		pm.restoreBackup(backupPath, serverPath)
		return fmt.Errorf("built binary not found at %s", newServer)
	}

	pm.sendUpgradeEvent(c, "replacing", "replacing current binary")
	if err := os.Rename(newServer, serverPath); err != nil {
		os.RemoveAll(workspace)
		pm.sendUpgradeEvent(c, "rollback", "rename failed — restoring backup")
		pm.restoreBackup(backupPath, serverPath)
		return fmt.Errorf("rename: %w", err)
	}

	if err := os.Chmod(serverPath, 0o755); err != nil {
		os.RemoveAll(workspace)
		pm.sendUpgradeEvent(c, "rollback", "chmod failed — restoring backup")
		pm.restoreBackup(backupPath, serverPath)
		return fmt.Errorf("chmod: %w", err)
	}

	// Clean up workspace.
	os.RemoveAll(workspace)

	pm.sendUpgradeEvent(c, "smoke-check", "running post-upgrade smoke test")
	if err := pm.smokeTest(serverPath); err != nil {
		pm.sendUpgradeEvent(c, "rollback", fmt.Sprintf("smoke test failed — restoring backup: %v", err))
		pm.restoreBackup(backupPath, serverPath)
		return fmt.Errorf("smoke test failed: %w", err)
	}

	pm.sendUpgradeEvent(c, "restart", "restarting llama-server process")
	if err := pm.restartLlamaServer(); err != nil {
		pm.sendUpgradeEvent(c, "rollback", fmt.Sprintf("restart failed — restoring backup: %v", err))
		pm.restoreBackup(backupPath, serverPath)
		return fmt.Errorf("restart: %w", err)
	}

	pm.sendUpgradeEvent(c, "complete", "upgrade from source complete — llama-server restarted")
	return nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// currentServerPath returns the path to the currently running llama-server binary.
func (pm *ProxyManager) currentServerPath() (string, error) {
	// Try to find the running server process via pgrep.
	cmd := exec.Command("pgrep", "-a", "llama-server")
	out, err := cmd.Output()
	if err == nil {
		// pgrep -a outputs "PID command_line"
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) > 0 {
			fields := strings.Fields(lines[0])
			if len(fields) >= 2 {
				// The last field is typically the binary path.
				return fields[len(fields)-1], nil
			}
		}
	}
	// Fallback: use config models dir to construct a default path.
	modelsDir := pm.modelsDir()
	if modelsDir != "" {
		return filepath.Join(modelsDir, "llama-server"), nil
	}
	return "", fmt.Errorf("cannot determine current llama-server path")
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	if err != nil {
		return err
	}
	return out.Close()
}

func (pm *ProxyManager) restoreBackup(backupPath, targetPath string) {
	if _, err := os.Stat(backupPath); err == nil {
		if err := os.Rename(backupPath, targetPath); err != nil {
			pm.proxyLogger.Warnf("restore backup failed: %v", err)
		}
	}
}

func (pm *ProxyManager) smokeTest(serverPath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, serverPath, "--version")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("smoke test failed: exit %d: %s", cmd.ProcessState.ExitCode(), string(out))
	}
	return nil
}

func (pm *ProxyManager) restartLlamaServer() error {
	// Find the running llama-server process and send it SIGHUP or SIGTERM.
	cmd := exec.Command("pgrep", "-a", "llama-server")
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("no llama-server process found to restart")
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 1 {
			continue
		}
		pid, _ := strconv.Atoi(fields[0])
		if pid > 0 {
			proc, err := os.FindProcess(pid)
			if err == nil {
				_ = proc.Signal(os.Interrupt)
			}
		}
	}

	// Wait briefly for the process to exit.
	time.Sleep(2 * time.Second)
	return nil
}
