package server

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
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

// unloadAllModels stops every locally-running model before an upgrade swaps
// the llama-server binary, so the new binary takes effect immediately for the
// next request instead of an old (possibly wedged) process lingering until it
// happens to restart on its own.
func (s *Server) unloadAllModels(w http.ResponseWriter) {
	if s.local == nil {
		return
	}
	running := s.local.RunningModels()
	if len(running) == 0 {
		return
	}
	s.sendUpgradeEvent(w, "unloading", fmt.Sprintf("stopping %d running model(s) before upgrade", len(running)))
	s.local.Unload(30 * time.Second)
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

// githubAsset is the subset of a GitHub release asset this file needs.
type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type githubRelease struct {
	TagName string        `json:"tag_name"`
	Assets  []githubAsset `json:"assets"`
}

// prebuiltSource describes where to fetch a prebuilt llama-server release
// from and how to recognize the right asset in that release's asset list.
// Returned by resolvePrebuiltSource (pure, unit-testable); the network I/O
// and extraction live in upgradePrebuilt.
type prebuiltSource struct {
	repo       string // GitHub "owner/repo"
	matchAsset func(name string) bool
	archiveFmt string // "zip" or "tar.gz"
	tailored   bool   // true when this is a build tuned for the specific detected GPU
	note       string // human-readable explanation, surfaced as a progress event
}

// lemonadeGfxBucket maps a detected AMD gfx target to lemonade-sdk/llamacpp-rocm's
// release asset naming (see docs/manual_instructions.md and their release asset
// list, e.g. llama-<tag>-ubuntu-rocm-gfx110X-x64.zip). Some architectures are
// bucketed under a wildcard-last-digit name (gfx110X covers gfx1100-gfx1103);
// others are published under their exact name. ok is false when the arch isn't
// one of their currently-published buckets, so callers can fall back cleanly.
func lemonadeGfxBucket(gfx string) (bucket string, ok bool) {
	switch gfx {
	case "gfx1100", "gfx1101", "gfx1102", "gfx1103": // RDNA3: 7900 XTX/XT, W7900/W7800/W7700/W7600
		return "gfx110X", true
	case "gfx1200", "gfx1201": // RDNA4: RX 9070/9070 XT, R9700, Strix Halo
		return "gfx120X", true
	case "gfx1030", "gfx1031", "gfx1032", "gfx1033", "gfx1034", "gfx1035", "gfx1036": // RDNA2
		return "gfx103X", true
	case "gfx1150", "gfx1151", "gfx90a", "gfx908": // published under their exact name, not bucketed
		return gfx, true
	default:
		return "", false
	}
}

// resolvePrebuiltSource decides where to fetch a prebuilt llama-server build
// from, given the AMD GPU architecture detected via sysfs (gfx == "" means
// none was found). Deliberately NOT gated on a ROCm *dev toolchain* being
// present (detectROCmRoot/detectROCm, which look for hipcc): that's only
// needed to compile from source. A prebuilt binary needs zero local
// toolchain — that's the whole point of prebuilt — and rocky is the proof:
// it runs ROCm inference (via runtime libs borrowed from Ollama's bundle)
// with no hipcc anywhere on the host, so gating prebuilt on detectROCm()
// wrongly fell through to a CPU build there. gfx (from the sysfs-only
// tuning.DetectGfx, already resolved into s.tunedGfx at startup) is the
// correct, toolchain-independent signal for "this host has an AMD GPU".
//
// When the detected GPU has a lemonade-sdk tailored bucket, that build is
// ALWAYS preferred: lemonade-sdk/llamacpp-rocm builds RDNA3/RDNA4 with
// -DGGML_HIP_ROCWMMA_FATTN=OFF by design (see z4's wedge investigation — the
// WMMA flash-attention kernel path is unstable on RDNA3 for large head
// dimensions; disabling it is their deliberate, tailored configuration, not
// a fallback). An AMD GPU whose arch lemonade-sdk doesn't publish falls back
// to upstream llama.cpp's own generic ROCm build — which is NOT
// arch-tailored and may carry the same instability; the note says so. No
// detected GPU gets upstream's plain CPU build.
func resolvePrebuiltSource(gfx string) prebuiltSource {
	if gfx != "" {
		if bucket, ok := lemonadeGfxBucket(gfx); ok {
			// lemonade-sdk publishes both Windows and Ubuntu assets with the
			// same "-rocm-<bucket>-x64.zip" suffix (e.g. "...-win-rocm-..."
			// vs "...-ubuntu-rocm-...") — require "-ubuntu-" too, or this
			// would pick the Windows asset just as happily.
			suffix := fmt.Sprintf("-rocm-%s-x64.zip", bucket)
			return prebuiltSource{
				repo: "lemonade-sdk/llamacpp-rocm",
				matchAsset: func(name string) bool {
					return strings.Contains(name, "-ubuntu-") && strings.HasSuffix(name, suffix)
				},
				archiveFmt: "zip",
				tailored:   true,
				note:       fmt.Sprintf("using lemonade-sdk/llamacpp-rocm's %s-tailored build (ROCWMMA_FATTN off by design — see z4-wedge-rootcause)", bucket),
			}
		}
		return prebuiltSource{
			repo: "ggml-org/llama.cpp",
			matchAsset: func(name string) bool {
				return strings.Contains(name, "-bin-ubuntu-rocm-") && strings.HasSuffix(name, "-x64.tar.gz")
			},
			archiveFmt: "tar.gz",
			tailored:   false,
			note:       fmt.Sprintf("no lemonade-sdk tailored build for detected GPU arch %q — falling back to upstream's generic ROCm build (NOT arch-tailored; may carry the same RDNA3/RDNA4 flash-attention instability)", gfx),
		}
	}
	return prebuiltSource{
		repo:       "ggml-org/llama.cpp",
		matchAsset: func(name string) bool { return strings.HasSuffix(name, "-bin-ubuntu-x64.tar.gz") },
		archiveFmt: "tar.gz",
		tailored:   false,
		note:       "CPU build (no ROCm detected)",
	}
}

// fetchGithubRelease resolves ref ("latest" or a specific tag) to a release
// and its asset list for repo.
func fetchGithubRelease(ctx context.Context, repo, ref string) (githubRelease, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo)
	if ref != "" && ref != "latest" {
		url = fmt.Sprintf("https://api.github.com/repos/%s/releases/tags/%s", repo, ref)
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return githubRelease{}, err
	}
	hreq.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(hreq)
	if err != nil {
		return githubRelease{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return githubRelease{}, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	var rel githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return githubRelease{}, fmt.Errorf("decode release: %w", err)
	}
	return rel, nil
}

// selectReleaseAsset returns the first asset in assets matching source's
// criteria. Separated from fetchGithubRelease so the selection logic is
// testable against a fixed asset list without hitting the network.
func selectReleaseAsset(assets []githubAsset, source prebuiltSource) (githubAsset, bool) {
	for _, a := range assets {
		if source.matchAsset(a.Name) {
			return a, true
		}
	}
	return githubAsset{}, false
}

// downloadFile streams url to destPath.
func downloadFile(ctx context.Context, url, destPath string) error {
	hreq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(hreq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

// extractArchive extracts a zip or tar.gz archive at archivePath into destDir,
// flattening any single top-level directory the archive wraps everything in
// (both lemonade-sdk's zips and llama.cpp's tarballs do this). Uses only the
// standard library (archive/zip, archive/tar, compress/gzip) — no external
// unzip/tar binary dependency, since it can't be assumed present (z4's LXC
// shipped without one).
func extractArchive(archivePath, destDir, format string) error {
	switch format {
	case "zip":
		return extractZip(archivePath, destDir)
	case "tar.gz":
		return extractTarGz(archivePath, destDir)
	default:
		return fmt.Errorf("unknown archive format %q", format)
	}
}

func extractZip(archivePath, destDir string) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, f := range zr.File {
		if err := extractArchiveEntry(f.Name, f.FileInfo().IsDir(), f.Mode(), destDir, func() (io.ReadCloser, error) { return f.Open() }); err != nil {
			return err
		}
	}
	return nil
}

func extractTarGz(archivePath, destDir string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeDir {
			continue // skip symlinks etc.
		}
		entry := tr
		if err := extractArchiveEntry(hdr.Name, hdr.Typeflag == tar.TypeDir, hdr.FileInfo().Mode(), destDir, func() (io.ReadCloser, error) { return io.NopCloser(entry), nil }); err != nil {
			return err
		}
	}
}

// stripTopLevelDir removes a leading "some-dir/" component from name, if
// present, so an archive that wraps everything in one top-level directory
// (both lemonade-sdk's and llama.cpp's releases do this) extracts flat into
// destDir rather than nesting an extra directory level.
func stripTopLevelDir(name string) string {
	if _, rest, ok := strings.Cut(name, "/"); ok {
		return rest
	}
	return name
}

func extractArchiveEntry(name string, isDir bool, mode os.FileMode, destDir string, open func() (io.ReadCloser, error)) error {
	rel := stripTopLevelDir(name)
	if rel == "" {
		return nil
	}
	target := filepath.Join(destDir, rel)
	// Guard against a malicious/malformed archive entry escaping destDir.
	if !strings.HasPrefix(target, filepath.Clean(destDir)+string(os.PathSeparator)) {
		return fmt.Errorf("archive entry %q escapes destination", name)
	}
	if isDir {
		return os.MkdirAll(target, 0o755)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	rc, err := open()
	if err != nil {
		return err
	}
	defer rc.Close()
	perm := mode.Perm()
	if perm == 0 {
		perm = 0o644
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, rc)
	return err
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

	source := resolvePrebuiltSource(s.tunedGfx)
	s.sendUpgradeEvent(w, "source", source.note)

	s.sendUpgradeEvent(w, "resolving", fmt.Sprintf("resolving %s release %s", source.repo, ref))
	rel, err := fetchGithubRelease(r.Context(), source.repo, ref)
	if err != nil {
		return fmt.Errorf("resolve release: %w", err)
	}
	asset, ok := selectReleaseAsset(rel.Assets, source)
	if !ok {
		return fmt.Errorf("no matching release asset found in %s release %s", source.repo, rel.TagName)
	}

	workspace := filepath.Join(os.TempDir(), "llama-skein-upgrade-prebuilt")
	if err := os.RemoveAll(workspace); err != nil {
		return fmt.Errorf("clean workspace: %w", err)
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return fmt.Errorf("create workspace: %w", err)
	}
	defer os.RemoveAll(workspace)

	s.sendUpgradeEvent(w, "downloading", fmt.Sprintf("fetching %s (%s)", asset.Name, rel.TagName))
	archivePath := filepath.Join(workspace, asset.Name)
	if err := downloadFile(r.Context(), asset.BrowserDownloadURL, archivePath); err != nil {
		s.sendUpgradeEvent(w, "rollback", "download failed — restoring backup")
		s.restoreBackup(w, backupPath, serverPath)
		return fmt.Errorf("download %s: %w", asset.Name, err)
	}

	s.sendUpgradeEvent(w, "extracting", "extracting archive")
	extractDir := filepath.Join(workspace, "extracted")
	if err := extractArchive(archivePath, extractDir, source.archiveFmt); err != nil {
		s.sendUpgradeEvent(w, "rollback", "extraction failed — restoring backup")
		s.restoreBackup(w, backupPath, serverPath)
		return fmt.Errorf("extract %s: %w", asset.Name, err)
	}

	newServer := filepath.Join(extractDir, "llama-server")
	if _, err := os.Stat(newServer); os.IsNotExist(err) {
		s.sendUpgradeEvent(w, "rollback", "llama-server not found in archive — restoring backup")
		s.restoreBackup(w, backupPath, serverPath)
		return fmt.Errorf("llama-server not found in extracted archive")
	}
	if err := os.Chmod(newServer, 0o755); err != nil {
		return fmt.Errorf("chmod extracted binary: %w", err)
	}

	// Copy every bundled shared library (ROCm runtime, ggml, etc.) alongside
	// the binary. These packages are self-contained specifically so they
	// don't depend on — or conflict with — whatever ROCm version the host
	// already has installed.
	libDir := filepath.Dir(serverPath)
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		return fmt.Errorf("create lib dir %s: %w", libDir, err)
	}
	s.sendUpgradeEvent(w, "libs", fmt.Sprintf("copying bundled shared libraries to %s", libDir))
	if err := copySharedLibs(extractDir, libDir); err != nil {
		s.sendUpgradeEvent(w, "warn", fmt.Sprintf("shared lib copy partial: %v", err))
	}

	s.unloadAllModels(w)

	s.sendUpgradeEvent(w, "replacing", "replacing current binary")
	if err := safeReplaceBinary(newServer, serverPath); err != nil {
		s.sendUpgradeEvent(w, "rollback", "replace failed — restoring backup")
		s.restoreBackup(w, backupPath, serverPath)
		return fmt.Errorf("replace binary: %w", err)
	}
	selinuxRelabel(serverPath)

	s.sendUpgradeEvent(w, "smoke-check", "running post-upgrade smoke test")
	if err := smokeTest(serverPath, libDir); err != nil {
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

	// Unload before swapping: a running llama-server keeps its OLD binary's
	// code mapped until it exits, so an upgrade that doesn't unload first
	// silently leaves the old (possibly wedged) process serving until the
	// next natural restart. safeReplaceBinary's rename is technically safe
	// against a still-running process (ETXTBSY only hits an open-for-write),
	// but leaving the model loaded through an upgrade is the wrong semantics.
	s.unloadAllModels(w)

	s.sendUpgradeEvent(w, "replacing", "replacing current binary")
	if err := safeReplaceBinary(newServer, serverPath); err != nil {
		os.RemoveAll(workspace)
		s.sendUpgradeEvent(w, "rollback", "replace failed — restoring backup")
		s.restoreBackup(w, backupPath, serverPath)
		return fmt.Errorf("replace binary: %w", err)
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

// detectROCmRoot returns the ROCm dev-toolchain root (where hipcc/amdclang++
// live) if found, or "". Only meaningful for compiling from source
// (sourceCmakeArgs) — a prebuilt binary needs no local toolchain at all, so
// resolvePrebuiltSource uses the sysfs-only tuning.DetectGfx signal
// (s.tunedGfx) instead; gating prebuilt on this check wrongly treated a host
// with GPU runtime libs but no dev toolchain (rocky) as CPU-only.
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

// safeReplaceBinary installs newPath as targetPath without ever open()-for-write
// onto a file that may currently be memory-mapped/executing. A direct copyFile
// onto a running binary fails with ETXTBSY ("text file busy") — this hit in
// production when upgradeFromSource copied straight onto serverPath while a
// model was still loaded. Writing to a temp file in the SAME directory (so the
// final rename stays on one filesystem) and renaming into place is atomic and
// safe regardless of whether the old binary is currently running: rename only
// repoints the directory entry, it never touches the inode a running process
// already has open.
func safeReplaceBinary(newPath, targetPath string) error {
	dir := filepath.Dir(targetPath)
	tmp, err := os.CreateTemp(dir, filepath.Base(targetPath)+".upgrade-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once the rename below succeeds

	src, err := os.Open(newPath)
	if err != nil {
		tmp.Close()
		return fmt.Errorf("open new binary: %w", err)
	}
	_, copyErr := io.Copy(tmp, src)
	src.Close()
	closeErr := tmp.Close()
	if copyErr != nil {
		return fmt.Errorf("copy to temp: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close temp: %w", closeErr)
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return fmt.Errorf("chmod temp: %w", err)
	}
	if err := os.Rename(tmpPath, targetPath); err != nil {
		return fmt.Errorf("rename into place: %w", err)
	}
	return nil
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
