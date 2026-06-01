package server

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/androidand/llama-skein/internal/config"
	"gopkg.in/yaml.v3"
)

// readYAMLRoot parses the config YAML at path and returns the root mapping node.
// Comments, key ordering, and node styles are fully preserved.
func readYAMLRoot(path string) (*yaml.Node, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if doc.Kind == 0 || len(doc.Content) == 0 {
		return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}, nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("expected mapping at root of %s", path)
	}
	return root, nil
}

// writeYAMLRoot wraps root in a document node and atomically writes the YAML.
func writeYAMLRoot(path string, root *yaml.Node, perm os.FileMode) error {
	doc := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}
	out, err := yaml.Marshal(doc)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return atomicWriteFile(path, out, perm)
}

// atomicWriteFile writes data to path via temp file + rename.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// yamlMapGet returns the value node for key in a mapping node, or nil.
func yamlMapGet(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// yamlMapSet sets key=val in a mapping node, appending if absent.
func yamlMapSet(m *yaml.Node, key string, val *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = val
			return
		}
	}
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: key, Tag: "!!str"},
		val,
	)
}

// yamlMapDelete removes key from a mapping node.
func yamlMapDelete(m *yaml.Node, key string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return
		}
	}
}

func yamlScalar(s string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Value: s, Tag: "!!str"}
}

func yamlInt(n int) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Value: fmt.Sprint(n), Tag: "!!int"}
}

func yamlBool(b bool) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Value: fmt.Sprint(b), Tag: "!!bool"}
}

// isValidModelID rejects IDs with characters that would break YAML keys or route matching.
func isValidModelID(id string) bool {
	if len(id) == 0 {
		return false
	}
	for _, c := range id {
		if !('A' <= c && c <= 'Z') && !('a' <= c && c <= 'z') && !('0' <= c && c <= '9') &&
			c != '.' && c != '_' && c != ':' && c != '/' && c != '-' {
			return false
		}
	}
	return true
}

func normalizeCmdFlag(flag string) string {
	flag = strings.TrimSpace(flag)
	flag = strings.TrimPrefix(flag, "--")
	return "--" + strings.ReplaceAll(flag, "_", "-")
}

func flagValueString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return fmt.Sprintf("%g", x)
	case bool:
		if x {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprint(x)
	}
}

func patchCommandFlags(cmd string, flags map[string]string) (string, error) {
	parts, err := config.SanitizeCommand(cmd)
	if err != nil {
		return "", err
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("cmd is empty")
	}

	keys := make([]string, 0, len(flags))
	for k := range flags {
		keys = append(keys, normalizeCmdFlag(k))
	}
	sort.Strings(keys)

	for _, flag := range keys {
		value := strings.TrimSpace(flags[flag])
		if value == "" {
			continue
		}
		found := false
		for i := 0; i < len(parts); i++ {
			if parts[i] == flag && i+1 < len(parts) {
				parts[i+1] = value
				found = true
				break
			}
			if strings.HasPrefix(parts[i], flag+"=") {
				parts[i] = flag + "=" + value
				found = true
				break
			}
		}
		if !found {
			parts = append(parts, flag, value)
		}
	}
	return strings.Join(parts, " "), nil
}
