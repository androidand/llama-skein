package server

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

	"github.com/androidand/llama-skein/internal/router"
)

type upgradeRequest struct {
	Method string `json:"method"` // "prebuilt" or "source"
	Ref    string `json:"ref"`    // build tag like "b5142"
}

type upgradeProgressEvent struct {
	Event string `json:"event"`
	Msg   string `json:"message,omitempty"`
}

// handleAPIUpgrade implements POST /api/upgrade.
// Streams NDJSON progress while downloading/building and replacing llama-server.
func (s *Server) handleAPIUpgrade(w http.ResponseWriter, r *http.Request) {
	var req upgradeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		router.SendResponse(w, r, http.StatusBadRequest, err.Error())
		return
	}
	if req.Method == "" {
		router.SendResponse(w, r, http.StatusBadRequest, "method is required")
		return
	}
	if req.Method != "prebuilt" && req.Method != "source" {
		router.SendResponse(w, r, http.StatusBadRequest, "method must be 'prebuilt' or 'source'")
		return
	}
	if req.Ref == "" {
		router.SendResponse(w, r, http.StatusBadRequest, "ref is required")
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Accel-Buffering", "no")

	var err error
	switch req.Method {
	case "prebuilt":
		err = s.upgradePrebuilt(w, r, req.Ref)
	case "source":
		err = s.upgradeFromSource(w, r, req.Ref)
	}

	if err != nil {
		s.sendUpgradeEvent(w, "error", fmt.Sprintf("upgrade failed: %v", err))
		return
	}
	s.sendUpgradeEvent(w, "complete", fmt.Sprintf("upgrade to %s complete", req.Ref))
}

func (s *Server) sendUpgradeEvent(w http.ResponseWriter, event, msg string) {
	evt := upgradeProgressEvent{Event: event, Msg: msg}
	b, _ := json.Marshal(evt)
	w.Write(b)
	w.Write([]byte("\n"))
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *Server) upgradePrebuilt(w http.ResponseWriter, r *http.Request, ref string) error {
	serverPath, err := s.currentServerPath()
	if err != nil {
		return fmt.Errorf("determine server path: %w", err)
	}

	backupPath := serverPath + ".bak"
	if _, err := os.Stat(serverPath); err == nil {
		if copyErr := copyFile(serverPath, backupPath); copyErr != nil {
			return fmt.Errorf("backup current binary: %w", copyErr)
		}
	}

	s.sendUpgradeEvent(w, "downloading", fmt.Sprintf("fetching pre-built binary for %s", ref))

	downloadURL := fmt.Sprintf("https://github.com/ggerganov/llama.cpp/releases/download/%s/llama-server", ref)
	hreq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, downloadURL, nil)
	if err != nil {
		return fmt.Errorf("create download request: %w", err)
	}

	resp, err := http.DefaultClient.Do(hreq)
	if err != nil {
		s.sendUpgradeEvent(w, "rollback", "downloading failed — restoring backup")
		s.restoreBackup(w, backupPath, serverPath)
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		s.sendUpgradeEvent(w, "rollback", fmt.Sprintf("HTTP %d — restoring backup", resp.StatusCode))
		s.restoreBackup(w, backupPath, serverPath)
		return fmt.Errorf("download returned HTTP %d", resp.StatusCode)
	}

	tmpFile, err := os.CreateTemp(filepath.Dir(serverPath), "llama-server-upgrade-*")
	if err != nil {
		s.sendUpgradeEvent(w, "rollback", "creating temp file failed — restoring backup")
		s.restoreBackup(w, backupPath, serverPath)
		return fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	s.sendUpgradeEvent(w, "writing", "writing downloaded binary")
	if _, err := io.Copy(tmpFile, resp.Body); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		s.sendUpgradeEvent(w, "rollback", "writing binary failed — restoring backup")
		s.restoreBackup(w, backupPath, serverPath)
		return fmt.Errorf("write binary: %w", err)
	}
	tmpFile.Close()

	if err := os.Chmod(tmpPath, 0o755); err != nil {
		os.Remove(tmpPath)
		s.sendUpgradeEvent(w, "rollback", "chmod failed — restoring backup")
		s.restoreBackup(w, backupPath, serverPath)
		return fmt.Errorf("chmod: %w", err)
	}

	s.sendUpgradeEvent(w, "replacing", "replacing current binary")
	if err := os.Rename(tmpPath, serverPath); err != nil {
		os.Remove(tmpPath)
		s.sendUpgradeEvent(w, "rollback", "rename failed — restoring backup")
		s.restoreBackup(w, backupPath, serverPath)
		return fmt.Errorf("rename: %w", err)
	}

	s.sendUpgradeEvent(w, "smoke-check", "running post-upgrade smoke test")
	if err := smokeTest(serverPath); err != nil {
		s.sendUpgradeEvent(w, "rollback", fmt.Sprintf("smoke test failed — restoring backup: %v", err))
		s.restoreBackup(w, backupPath, serverPath)
		return fmt.Errorf("smoke test failed: %w", err)
	}

	s.sendUpgradeEvent(w, "restart", "restarting llama-server process")
	if err := restartLlamaServer(); err != nil {
		s.sendUpgradeEvent(w, "rollback", fmt.Sprintf("restart failed — restoring backup: %v", err))
		s.restoreBackup(w, backupPath, serverPath)
		return fmt.Errorf("restart: %w", err)
	}

	s.sendUpgradeEvent(w, "complete", "upgrade complete — llama-server restarted")
	return nil
}

func (s *Server) upgradeFromSource(w http.ResponseWriter, r *http.Request, ref string) error {
	serverPath, err := s.currentServerPath()
	if err != nil {
		return fmt.Errorf("determine server path: %w", err)
	}

	backupPath := serverPath + ".bak"
	if _, err := os.Stat(serverPath); err == nil {
		if copyErr := copyFile(serverPath, backupPath); copyErr != nil {
			return fmt.Errorf("backup current binary: %w", copyErr)
		}
	}

	modelsDir := s.modelsDir()
	if modelsDir == "" {
		return fmt.Errorf("models directory unknown; set modelsDir in config")
	}
	workspace := filepath.Join(modelsDir, ".upgrade-src")

	s.sendUpgradeEvent(w, "checkout", fmt.Sprintf("checking out ref %s in %s", ref, workspace))

	if _, err := os.Stat(filepath.Join(workspace, ".git")); os.IsNotExist(err) {
		s.sendUpgradeEvent(w, "cloning", "initial clone of llama.cpp")
		cmd := exec.CommandContext(r.Context(), "git", "clone", "https://github.com/ggerganov/llama.cpp", workspace)
		if out, err := cmd.CombinedOutput(); err != nil {
			os.RemoveAll(workspace)
			s.sendUpgradeEvent(w, "rollback", "git clone failed — restoring backup")
			s.restoreBackup(w, backupPath, serverPath)
			return fmt.Errorf("git clone: %w\n%s", err, string(out))
		}
	} else {
		s.sendUpgradeEvent(w, "fetching", "fetching latest refs")
		cmd := exec.CommandContext(r.Context(), "git", "-C", workspace, "fetch", "origin")
		if out, err := cmd.CombinedOutput(); err != nil {
			os.RemoveAll(workspace)
			s.sendUpgradeEvent(w, "rollback", "git fetch failed — restoring backup")
			s.restoreBackup(w, backupPath, serverPath)
			return fmt.Errorf("git fetch: %w\n%s", err, string(out))
		}
	}

	s.sendUpgradeEvent(w, "checkout-ref", fmt.Sprintf("checking out %s", ref))
	cmd := exec.CommandContext(r.Context(), "git", "-C", workspace, "checkout", ref)
	if out, err := cmd.CombinedOutput(); err != nil {
		os.RemoveAll(workspace)
		s.sendUpgradeEvent(w, "rollback", "git checkout failed — restoring backup")
		s.restoreBackup(w, backupPath, serverPath)
		return fmt.Errorf("git checkout: %w\n%s", err, string(out))
	}

	s.sendUpgradeEvent(w, "building", "compiling llama-server")
	cmd = exec.CommandContext(r.Context(), "make", "-C", workspace, "llama-server")
	if out, err := cmd.CombinedOutput(); err != nil {
		os.RemoveAll(workspace)
		s.sendUpgradeEvent(w, "rollback", fmt.Sprintf("build failed — restoring backup:\n%s", string(out)))
		s.restoreBackup(w, backupPath, serverPath)
		return fmt.Errorf("make: %w\n%s", err, string(out))
	}

	newServer := filepath.Join(workspace, "bin", "llama-server")
	if _, err := os.Stat(newServer); os.IsNotExist(err) {
		os.RemoveAll(workspace)
		s.sendUpgradeEvent(w, "rollback", "built binary not found — restoring backup")
		s.restoreBackup(w, backupPath, serverPath)
		return fmt.Errorf("built binary not found at %s", newServer)
	}

	s.sendUpgradeEvent(w, "replacing", "replacing current binary")
	if err := os.Rename(newServer, serverPath); err != nil {
		os.RemoveAll(workspace)
		s.sendUpgradeEvent(w, "rollback", "rename failed — restoring backup")
		s.restoreBackup(w, backupPath, serverPath)
		return fmt.Errorf("rename: %w", err)
	}
	if err := os.Chmod(serverPath, 0o755); err != nil {
		os.RemoveAll(workspace)
		s.sendUpgradeEvent(w, "rollback", "chmod failed — restoring backup")
		s.restoreBackup(w, backupPath, serverPath)
		return fmt.Errorf("chmod: %w", err)
	}
	os.RemoveAll(workspace)

	s.sendUpgradeEvent(w, "smoke-check", "running post-upgrade smoke test")
	if err := smokeTest(serverPath); err != nil {
		s.sendUpgradeEvent(w, "rollback", fmt.Sprintf("smoke test failed — restoring backup: %v", err))
		s.restoreBackup(w, backupPath, serverPath)
		return fmt.Errorf("smoke test failed: %w", err)
	}

	s.sendUpgradeEvent(w, "restart", "restarting llama-server process")
	if err := restartLlamaServer(); err != nil {
		s.sendUpgradeEvent(w, "rollback", fmt.Sprintf("restart failed — restoring backup: %v", err))
		s.restoreBackup(w, backupPath, serverPath)
		return fmt.Errorf("restart: %w", err)
	}

	s.sendUpgradeEvent(w, "complete", "upgrade from source complete — llama-server restarted")
	return nil
}

func (s *Server) currentServerPath() (string, error) {
	cmd := exec.Command("pgrep", "-a", "llama-server")
	out, err := cmd.Output()
	if err == nil {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) > 0 {
			fields := strings.Fields(lines[0])
			if len(fields) >= 2 {
				return fields[len(fields)-1], nil
			}
		}
	}
	modelsDir := s.modelsDir()
	if modelsDir != "" {
		return filepath.Join(modelsDir, "llama-server"), nil
	}
	return "", fmt.Errorf("cannot determine current llama-server path")
}

func (s *Server) restoreBackup(w http.ResponseWriter, backupPath, targetPath string) {
	if _, err := os.Stat(backupPath); err == nil {
		if err := os.Rename(backupPath, targetPath); err != nil {
			s.sendUpgradeEvent(w, "warn", fmt.Sprintf("restore backup failed: %v", err))
		}
	}
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

func smokeTest(serverPath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, serverPath, "--version")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("exit %d: %s", cmd.ProcessState.ExitCode(), string(out))
	}
	return nil
}

func restartLlamaServer() error {
	cmd := exec.Command("pgrep", "-a", "llama-server")
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("no llama-server process found to restart")
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
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
	time.Sleep(2 * time.Second)
	return nil
}
