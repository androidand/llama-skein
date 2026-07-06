package tuning

import (
	"regexp"
	"strings"
)

// mtpNameRe matches "MTP" as a token in a GGUF filename (…-MTP-…, MTP-…, …-MTP).
var mtpNameRe = regexp.MustCompile(`(?i)(^|[-_./])mtp([-_.]|$)`)

// IsMTPModel reports whether a model is MTP-capable. metadataMTPEnabled is the
// authoritative signal (the model's metadata.mtp.enabled); when it is false we
// fall back to a filename heuristic on the model command so a self-contained
// MTP GGUF (e.g. Qwopus…-MTP-Q8_0.gguf) is recognized without hand-set metadata.
func IsMTPModel(cmd string, metadataMTPEnabled bool) bool {
	if metadataMTPEnabled {
		return true
	}
	// Only inspect the --model/-m path token, not the whole command, to avoid
	// matching an unrelated directory.
	for _, path := range modelPaths(cmd) {
		if mtpNameRe.MatchString(path) {
			return true
		}
	}
	return false
}

func modelPaths(cmd string) []string {
	tokens := strings.Fields(cmd)
	var out []string
	for i, tok := range tokens {
		if (tok == "--model" || tok == "-m") && i+1 < len(tokens) {
			out = append(out, tokens[i+1])
		}
		if v, ok := strings.CutPrefix(tok, "--model="); ok {
			out = append(out, v)
		}
	}
	return out
}
