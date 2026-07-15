package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/androidand/llama-skein/internal/router"
)

type upgradeRequest struct {
	Method string `json:"method"` // "prebuilt" or "source"
	Ref    string `json:"ref"`    // build tag like "b9200"
	// RocwmmaFattn controls -DGGML_HIP_ROCWMMA_FATTN for a "source" build on
	// ROCm hosts. nil = upstream default (ON on RDNA3/CDNA where available).
	// false builds with the WMMA-based flash-attention kernel variant
	// disabled in favor of the generic kernel — a diagnostic lever for
	// suspected RDNA3 flash-attention instability. Ignored for non-ROCm/
	// "prebuilt" upgrades.
	RocwmmaFattn *bool `json:"rocwmmaFattn,omitempty"`
}

type upgradeProgressEvent struct {
	Event string `json:"event"`
	Msg   string `json:"message,omitempty"`
}

// runUpgrade implements POST /api/system/upgrade.
// Streams NDJSON progress while downloading/building and replacing llama-server.
func (s *Server) runUpgrade(w http.ResponseWriter, r *http.Request) {
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
		err = s.upgradeFromSource(w, r, req.Ref, req.RocwmmaFattn)
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
	selinuxRelabel(serverPath)

	s.sendUpgradeEvent(w, "smoke-check", "running post-upgrade smoke test")
	if err := smokeTest(serverPath, filepath.Dir(serverPath)); err != nil {
		s.sendUpgradeEvent(w, "rollback", fmt.Sprintf("smoke test failed — restoring backup: %v", err))
		s.restoreBackup(w, backupPath, serverPath)
		return fmt.Errorf("smoke test failed: %w", err)
	}

	s.sendUpgradeEvent(w, "restart", "restarting llama-server process")
	if err := restartLlamaServer(); err != nil {
		s.sendUpgradeEvent(w, "warn", fmt.Sprintf("restart: %v", err))
	}

	return nil
}

// sourceCmakeArgs builds the cmake configure argument list for a from-source
// llama-server build, plus human-readable notes to surface as progress
// events. Pure (no I/O) so the composition can be unit tested without
// mocking exec/filesystem. rocmRoot == "" means no ROCm toolchain was
// detected, so the build stays CPU/generic — gfx and rocwmmaFattn are both
// ignored in that case since AMDGPU_TARGETS and GGML_HIP_ROCWMMA_FATTN are
// meaningless without -DGGML_HIP=ON.
func sourceCmakeArgs(buildDir, workspace, rocmRoot, gfx string, rocwmmaFattn *bool) (args []string, notes []string) {
	args = []string{"-B", buildDir, "-DBUILD_SHARED_LIBS=ON", "-DLLAMA_SERVER_SSL=OFF", "-DLLAMA_BUILD_UI=OFF", "-DLLAMA_BUILD_WEBUI=OFF"}
	if rocmRoot != "" {
		// Tailor the build to the actual detected GPU rather than compiling
		// for HIP's broad default target list — faster build, and ensures
		// architecture-specific codegen (e.g. the WMMA kernels below) is for
		// the real hardware, not a generic fallback.
		if gfx != "" {
			args = append(args, "-DAMDGPU_TARGETS="+gfx)
			notes = append(notes, fmt.Sprintf("targeting detected GPU arch %s", gfx))
		}
		if rocwmmaFattn != nil {
			val := "ON"
			if !*rocwmmaFattn {
				val = "OFF"
			}
			args = append(args, "-DGGML_HIP_ROCWMMA_FATTN="+val)
			notes = append(notes, fmt.Sprintf("rocwmmaFattn override: -DGGML_HIP_ROCWMMA_FATTN=%s", val))
		}
		// cmake requires the real clang compiler, not the hipcc wrapper
		amdclang := filepath.Join(rocmRoot, "bin", "amdclang++")
		args = append(args, "-DGGML_HIP=ON", "-DCMAKE_HIP_COMPILER="+amdclang)
		notes = append(notes, fmt.Sprintf("ROCm detected at %s — adding -DGGML_HIP=ON", rocmRoot))
	}
	args = append(args, workspace)
	return args, notes
}

func (s *Server) upgradeFromSource(w http.ResponseWriter, r *http.Request, ref string, rocwmmaFattn *bool) error {
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

	// Use /tmp for the build tree: avoids NTFS locking issues on model mounts
	// and keeps the model storage clean. Always start fresh so there is no
	// stale cmake cache from a prior failed run.
	workspace := filepath.Join(os.TempDir(), "llama-skein-upgrade-src")
	if err := os.RemoveAll(workspace); err != nil {
		return fmt.Errorf("clean workspace: %w", err)
	}

	s.sendUpgradeEvent(w, "checkout", fmt.Sprintf("checking out ref %s in %s", ref, workspace))
	s.sendUpgradeEvent(w, "cloning", "cloning llama.cpp at "+ref)

	// shallow clone at the specific tag — no history needed
	cmd := exec.CommandContext(r.Context(), "git", "clone", "--depth", "1", "--branch", ref,
		"https://github.com/ggml-org/llama.cpp", workspace)
	if out, err := cmd.CombinedOutput(); err != nil {
		os.RemoveAll(workspace)
		s.sendUpgradeEvent(w, "rollback", fmt.Sprintf("git clone failed — restoring backup:\n%s", string(out)))
		s.restoreBackup(w, backupPath, serverPath)
		return fmt.Errorf("git clone: %w\n%s", err, string(out))
	}

	buildDir := filepath.Join(workspace, "build")
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		return fmt.Errorf("create build dir: %w", err)
	}

	// cmake configure
	s.sendUpgradeEvent(w, "configuring", "running cmake configure")
	rocmRoot := s.detectROCmRoot()
	cmakeArgs, rocmNotes := sourceCmakeArgs(buildDir, workspace, rocmRoot, s.tunedGfx, rocwmmaFattn)
	for _, n := range rocmNotes {
		s.sendUpgradeEvent(w, "rocm", n)
	}
	cmd = exec.CommandContext(r.Context(), "cmake", cmakeArgs...)
	// Ensure ROCm bin dir is on PATH for cmake's compiler detection
	if rocmRoot != "" {
		cmd.Env = append(os.Environ(), "PATH="+filepath.Join(rocmRoot, "bin")+":"+os.Getenv("PATH"))
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		os.RemoveAll(workspace)
		s.sendUpgradeEvent(w, "rollback", fmt.Sprintf("cmake configure failed — restoring backup:\n%s", string(out)))
		s.restoreBackup(w, backupPath, serverPath)
		return fmt.Errorf("cmake configure: %w\n%s", err, string(out))
	}

	// cmake build
	s.sendUpgradeEvent(w, "building", "compiling llama-server")
	nCPU := strconv.Itoa(runtime.NumCPU())
	cmd = exec.CommandContext(r.Context(), "cmake", "--build", buildDir, "--config", "Release", "-t", "llama-server", "-j", nCPU)
	if out, err := cmd.CombinedOutput(); err != nil {
		os.RemoveAll(workspace)
		s.sendUpgradeEvent(w, "rollback", fmt.Sprintf("build failed — restoring backup:\n%s", string(out)))
		s.restoreBackup(w, backupPath, serverPath)
		return fmt.Errorf("cmake build: %w\n%s", err, string(out))
	}

	// find the built binary (cmake puts it in build/bin/ or build/)
	newServer := filepath.Join(buildDir, "bin", "llama-server")
	if _, err := os.Stat(newServer); os.IsNotExist(err) {
		newServer = filepath.Join(buildDir, "llama-server")
		if _, err := os.Stat(newServer); os.IsNotExist(err) {
			os.RemoveAll(workspace)
			s.sendUpgradeEvent(w, "rollback", "built binary not found — restoring backup")
			s.restoreBackup(w, backupPath, serverPath)
			return fmt.Errorf("built binary not found at %s/bin/llama-server", buildDir)
		}
	}

	// copy shared libs to the binary's directory
	libDir := filepath.Dir(serverPath)
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		return fmt.Errorf("create lib dir %s: %w", libDir, err)
	}
	s.sendUpgradeEvent(w, "libs", fmt.Sprintf("copying shared libraries to %s", libDir))
	if err := copySharedLibs(buildDir, libDir); err != nil {
		s.sendUpgradeEvent(w, "warn", fmt.Sprintf("shared lib copy partial: %v", err))
	}

	s.sendUpgradeEvent(w, "replacing", "replacing current binary")
	if err := copyFile(newServer, serverPath); err != nil {
		os.RemoveAll(workspace)
		s.sendUpgradeEvent(w, "rollback", "copy failed — restoring backup")
		s.restoreBackup(w, backupPath, serverPath)
		return fmt.Errorf("copy binary: %w", err)
	}
	if err := os.Chmod(serverPath, 0o755); err != nil {
		os.RemoveAll(workspace)
		s.sendUpgradeEvent(w, "rollback", "chmod failed — restoring backup")
		s.restoreBackup(w, backupPath, serverPath)
		return fmt.Errorf("chmod: %w", err)
	}
	selinuxRelabel(serverPath)
	os.RemoveAll(workspace)

	s.sendUpgradeEvent(w, "smoke-check", "running post-upgrade smoke test")
	if err := smokeTest(serverPath, libDir); err != nil {
		s.sendUpgradeEvent(w, "rollback", fmt.Sprintf("smoke test failed — restoring backup: %v", err))
		s.restoreBackup(w, backupPath, serverPath)
		return fmt.Errorf("smoke test failed: %w", err)
	}

	s.sendUpgradeEvent(w, "restart", "restarting llama-server processes")
	if err := restartLlamaServer(); err != nil {
		s.sendUpgradeEvent(w, "warn", fmt.Sprintf("restart: %v", err))
	}

	return nil
}

// currentServerPath returns the filesystem path to the running llama-server binary.
// It inspects pgrep output first; falls back to the standard user install path.
func (s *Server) currentServerPath() (string, error) {
	cmd := exec.Command("pgrep", "-a", "llama-server")
	out, err := cmd.Output()
	if err == nil {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) > 0 {
			fields := strings.Fields(lines[0])
			// pgrep -a output: <PID> <binary> [args...]
			if len(fields) >= 2 {
				return fields[1], nil
			}
		}
	}
	// Fallback: standard user installation path
	if home, err := os.UserHomeDir(); err == nil {
		candidate := filepath.Join(home, ".local", "lib", "llama-cpp", "llama-server")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("cannot determine llama-server path: no running process and ~/.local/lib/llama-cpp/llama-server not found")
}

// detectROCmRoot returns the ROCm installation root if found, or "".
func (s *Server) detectROCmRoot() string {
	// Check standard path first
	if _, err := os.Stat("/opt/rocm/bin/hipcc"); err == nil {
		return "/opt/rocm"
	}
	// Check if hipcc is on PATH and resolve its parent's parent
	if p, err := exec.LookPath("hipcc"); err == nil {
		return filepath.Dir(filepath.Dir(p))
	}
	return ""
}

// detectROCm returns true if ROCm appears to be installed on this host.
func (s *Server) detectROCm() bool {
	return s.detectROCmRoot() != ""
}

// copySharedLibs walks srcDir recursively and copies every .so file into dstDir.
func copySharedLibs(srcDir, dstDir string) error {
	var errs []string
	err := filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".so") && !strings.Contains(name, ".so.") {
			return nil
		}
		dst := filepath.Join(dstDir, name)
		if copyErr := copyFile(path, dst); copyErr != nil {
			errs = append(errs, copyErr.Error())
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// selinuxRelabel runs chcon -t bin_t on path when chcon is available (Rocky Linux).
func selinuxRelabel(path string) {
	if _, err := exec.LookPath("chcon"); err != nil {
		return
	}
	_ = exec.Command("chcon", "-t", "bin_t", path).Run()
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
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

func smokeTest(serverPath, libDir string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, serverPath, "--version")
	if libDir != "" {
		existing := os.Getenv("LD_LIBRARY_PATH")
		cmd.Env = append(os.Environ(), "LD_LIBRARY_PATH="+libDir+":"+existing)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		if cmd.ProcessState != nil {
			return fmt.Errorf("exit %d: %s", cmd.ProcessState.ExitCode(), string(out))
		}
		return err
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
				_ = proc.Kill()
			}
		}
	}
	time.Sleep(2 * time.Second)
	return nil
}
