package tuning

import "strings"

// flagAliases groups llama-server flags that mean the same thing, so a profile
// flag is considered "already set" if the command uses any alias.
var flagAliases = map[string][]string{
	"--flash-attn": {"--flash-attn", "-fa"},
	"--parallel":   {"--parallel", "-np"},
	"--spec-type":  {"--spec-type"},
}

// hasFlag reports whether cmd already contains canonical (or any alias),
// either as a bare token or in `--flag=value` form.
func hasFlag(tokens []string, canonical string) bool {
	aliases := flagAliases[canonical]
	if aliases == nil {
		aliases = []string{canonical}
	}
	for _, tok := range tokens {
		for _, a := range aliases {
			if tok == a || strings.HasPrefix(tok, a+"=") {
				return true
			}
		}
	}
	return false
}

// ApplyProfile appends the profile's flags to cmd, adding only flags the
// command does not already set (an explicit flag always wins). MTP flags are
// added only when isMTP is true and the profile enables MTP for MTP models.
// extraArgs (from a user override) are appended under the same missing-flag
// rule. The function is pure and idempotent.
func ApplyProfile(cmd string, p Profile, isMTP bool, extraArgs []string) string {
	tokens := strings.Fields(cmd)
	var add []string

	if p.Flags.FlashAttn != nil && !hasFlag(tokens, "--flash-attn") {
		if *p.Flags.FlashAttn {
			add = append(add, "--flash-attn", "on")
		} else {
			add = append(add, "--flash-attn", "off")
		}
	}
	if p.Flags.Parallel != nil && !hasFlag(tokens, "--parallel") {
		add = append(add, "--parallel", itoa(*p.Flags.Parallel))
	}
	if isMTP && p.MTP != nil && p.MTP.ApplyToMTPModels && !hasFlag(tokens, "--spec-type") {
		add = append(add, "--spec-type", "draft-mtp")
		if p.MTP.DraftNMax > 0 {
			add = append(add, "--spec-draft-n-max", itoa(p.MTP.DraftNMax))
		}
		if p.MTP.DraftPMin > 0 {
			add = append(add, "--draft-p-min", ftoa(p.MTP.DraftPMin))
		}
	}

	// extraArgs: append verbatim, but skip any whose leading flag token is
	// already present so re-applying stays idempotent.
	for i := 0; i < len(extraArgs); i++ {
		arg := extraArgs[i]
		if strings.HasPrefix(arg, "-") {
			name, _, hasEq := strings.Cut(arg, "=")
			if hasFlag(tokens, name) || containsToken(add, name) {
				// skip this flag and a following value token if present
				if !hasEq && i+1 < len(extraArgs) && !strings.HasPrefix(extraArgs[i+1], "-") {
					i++
				}
				continue
			}
		}
		add = append(add, arg)
	}

	if len(add) == 0 {
		return cmd
	}
	if strings.TrimSpace(cmd) == "" {
		return strings.Join(add, " ")
	}
	return cmd + " " + strings.Join(add, " ")
}

func containsToken(s []string, tok string) bool {
	for _, x := range s {
		if x == tok || strings.HasPrefix(x, tok+"=") {
			return true
		}
	}
	return false
}
