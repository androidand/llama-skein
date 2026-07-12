package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestConfig_MaxRequestTimeSecsInheritance(t *testing.T) {
	content := `
maxRequestTimeSecs: 900
models:
  inherits:
    cmd: llama-server --model /m/a.gguf --port ${PORT}
  ownValue:
    maxRequestTimeSecs: 120
    cmd: llama-server --model /m/b.gguf --port ${PORT}
`
	config, err := LoadConfigFromReader(strings.NewReader(content))
	assert.NoError(t, err)
	assert.Equal(t, 900, config.Models["inherits"].MaxRequestTimeSecs,
		"a model without its own cap inherits the global maxRequestTimeSecs")
	assert.Equal(t, 120, config.Models["ownValue"].MaxRequestTimeSecs,
		"a model's own maxRequestTimeSecs wins over the global")
}

func TestConfig_MaxRequestTimeSecsNoGlobalKeepsZero(t *testing.T) {
	content := `
models:
  m:
    cmd: llama-server --model /m/a.gguf --port ${PORT}
`
	config, err := LoadConfigFromReader(strings.NewReader(content))
	assert.NoError(t, err)
	assert.Equal(t, 0, config.Models["m"].MaxRequestTimeSecs,
		"without a global default, no limit is applied")
}
