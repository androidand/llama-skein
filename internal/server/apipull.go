package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/router"
)

type pullRequest struct {
	Model    string       `json:"model"`
	Token    string       `json:"token"`
	Stream   *bool        `json:"stream"`
	Subdir   string       `json:"subdir"`
	Register *pullRegister `json:"register"`
}

type pullRegister struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Flags       string `json:"flags"`
	TTL         *int   `json:"ttl"`
}

type pullProgress struct {
	Status    string `json:"status"`
	Filename  string `json:"filename,omitempty"`
	Total     int64  `json:"total,omitempty"`
	Completed int64  `json:"completed,omitempty"`
	Path      string `json:"path,omitempty"`
	Error     string `json:"error,omitempty"`
}

// isHuggingFaceHost returns true if host is huggingface.co or a subdomain.
func isHuggingFaceHost(host string) bool {
	h := strings.ToLower(host)
	return h == "huggingface.co" || strings.HasSuffix(h, ".huggingface.co")
}

// resolveHFSource parses a model identifier into a download URL and filename.
func resolveHFSource(model string) (downloadURL, filename string, err error) {
	if strings.HasPrefix(model, "https://") || strings.HasPrefix(model, "http://") {
		u, parseErr := url.Parse(model)
		if parseErr != nil {
			return "", "", fmt.Errorf("invalid URL: %v", parseErr)
		}
		host := strings.ToLower(u.Hostname())
		isLoopback := host == "localhost" || host == "127.0.0.1" || host == "::1"
		if !isLoopback {
			if u.Scheme != "https" {
				return "", "", fmt.Errorf("only HTTPS URLs are supported for remote downloads")
			}
			if !isHuggingFaceHost(host) {
				return "", "", fmt.Errorf("URL host %q is not allowed; only huggingface.co domains are supported", host)
			}
		}
		clean := model
		if idx := strings.Index(clean, "?"); idx != -1 {
			clean = clean[:idx]
		}
		filename = filepath.Base(clean)
		if filename == "" || filename == "." || filename == ".." || strings.ContainsAny(filename, `/\`) {
			return "", "", fmt.Errorf("cannot derive filename from URL %q", model)
		}
		downloadURL = model
		return
	}
	parts := strings.SplitN(model, "/", 3)
	if len(parts) != 3 {
		return "", "", fmt.Errorf("model must be 'owner/repo/filename.gguf' or a full HuggingFace HTTPS URL, got %q", model)
	}
	owner, repo, file := parts[0], parts[1], parts[2]
	filename = filepath.Base(file)
	downloadURL = fmt.Sprintf("https://huggingface.co/%s/%s/resolve/main/%s", owner, repo, file)
	return
}

func hfToken(reqToken string) string {
	if reqToken != "" {
		return reqToken
	}
	return os.Getenv("HF_TOKEN")
}

// handleAPIPullModel downloads a model from HuggingFace to the models
// directory, streaming NDJSON progress. POST /api/models/pull
func (s *Server) handleAPIPullModel(w http.ResponseWriter, r *http.Request) {
	var req pullRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		router.SendResponse(w, r, http.StatusBadRequest, err.Error())
		return
	}
	if req.Model == "" {
		router.SendResponse(w, r, http.StatusBadRequest, "model field is required")
		return
	}

	baseDir := s.modelsDir()
	if baseDir == "" {
		router.SendResponse(w, r, http.StatusUnprocessableEntity,
			"models directory unknown; set modelsDir in config")
		return
	}

	dir := baseDir
	if req.Subdir != "" {
		clean := filepath.Clean(req.Subdir)
		if strings.Contains(clean, "..") || filepath.IsAbs(clean) {
			router.SendResponse(w, r, http.StatusBadRequest, "invalid subdir: path traversal detected")
			return
		}
		dir = filepath.Join(baseDir, clean)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			router.SendResponse(w, r, http.StatusInternalServerError,
				fmt.Sprintf("create subdir: %v", err))
			return
		}
	}

	downloadURL, filename, err := resolveHFSource(req.Model)
	if err != nil {
		router.SendResponse(w, r, http.StatusBadRequest, err.Error())
		return
	}

	token := ""
	if u, parseErr := url.Parse(downloadURL); parseErr == nil && isHuggingFaceHost(u.Hostname()) {
		token = hfToken(req.Token)
	}

	hreq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, downloadURL, nil)
	if err != nil {
		router.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	if token != "" {
		hreq.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(hreq)
	if err != nil {
		router.SendResponse(w, r, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		msg := fmt.Sprintf("HuggingFace returned %d — model may be gated; provide a token", resp.StatusCode)
		router.SendResponse(w, r, resp.StatusCode, msg)
		return
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		router.SendResponse(w, r, http.StatusBadGateway,
			fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(b)))
		return
	}

	stream := req.Stream == nil || *req.Stream
	if stream {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Accel-Buffering", "no")
	}

	flusher, _ := w.(http.Flusher)
	writeOk := true
	sendJSON := func(v pullProgress) {
		if !writeOk || !stream {
			return
		}
		b, _ := json.Marshal(v)
		if _, err := w.Write(b); err != nil {
			writeOk = false
			return
		}
		if _, err := w.Write([]byte("\n")); err != nil {
			writeOk = false
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
	}

	dest := filepath.Join(dir, filename)
	rel, relErr := filepath.Rel(filepath.Clean(dir), filepath.Clean(dest))
	if relErr != nil || strings.HasPrefix(rel, "..") {
		router.SendResponse(w, r, http.StatusBadRequest, "invalid filename: path traversal detected")
		return
	}

	tmp := dest + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		router.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}

	total := resp.ContentLength
	var completed int64
	buf := make([]byte, 32*1024)
	lastReport := int64(0)

	sendJSON(pullProgress{Status: "downloading", Filename: filename, Total: total, Completed: 0})

	for {
		if !writeOk || r.Context().Err() != nil {
			f.Close()
			os.Remove(tmp)
			return
		}
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := f.Write(buf[:n]); writeErr != nil {
				f.Close()
				os.Remove(tmp)
				sendJSON(pullProgress{Status: "error", Error: writeErr.Error()})
				if !stream {
					router.SendResponse(w, r, http.StatusInternalServerError, writeErr.Error())
				}
				return
			}
			completed += int64(n)
			if completed-lastReport >= 10*1024*1024 || readErr == io.EOF {
				sendJSON(pullProgress{Status: "downloading", Filename: filename, Total: total, Completed: completed})
				lastReport = completed
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			f.Close()
			os.Remove(tmp)
			sendJSON(pullProgress{Status: "error", Error: readErr.Error()})
			if !stream {
				router.SendResponse(w, r, http.StatusInternalServerError, readErr.Error())
			}
			return
		}
	}

	if err := f.Close(); err != nil {
		os.Remove(tmp)
		sendJSON(pullProgress{Status: "error", Error: err.Error()})
		if !stream {
			router.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		}
		return
	}

	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		sendJSON(pullProgress{Status: "error", Error: err.Error()})
		if !stream {
			router.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		}
		return
	}

	sendJSON(pullProgress{Status: "success", Filename: filename, Path: dest})

	if req.Register != nil {
		if s.configFile == "" {
			sendJSON(pullProgress{Status: "register_failed", Error: "config file path not set; cannot auto-register"})
		} else {
			reg := req.Register
			id := reg.ID
			if id == "" {
				base := filename
				if strings.HasSuffix(base, ".gguf") {
					base = base[:len(base)-5]
				}
				id = strings.ToLower(base)
			}
			cmd := s.buildCmd(dest, reg.Flags)
			mc := config.ModelConfig{
				Cmd:         cmd,
				Proxy:       "http://localhost:${PORT}",
				Name:        reg.Name,
				Description: reg.Description,
				UnloadAfter: config.MODEL_CONFIG_DEFAULT_TTL,
			}
			if reg.TTL != nil {
				mc.UnloadAfter = *reg.TTL
			}
			if writeErr := s.writeModelToConfig(id, &mc); writeErr == nil {
				sendJSON(pullProgress{Status: "registered", Filename: id, Path: dest})
				s.triggerReload()
			} else {
				sendJSON(pullProgress{Status: "register_failed", Error: writeErr.Error()})
			}
		}
	}

	if !stream {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"status": "success", "filename": filename, "path": dest})
	}
}
