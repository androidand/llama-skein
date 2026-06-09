package config

import (
	"fmt"
	"strings"
)

func applyBackendDefaults(modelID string, mc *ModelConfig) error {
	mc.Backend = strings.ToLower(strings.TrimSpace(mc.Backend))
	if mc.Backend == "" || strings.TrimSpace(mc.UseModelName) != "" {
		return nil
	}

	inferred, ok, err := inferUseModelName(*mc)
	if err != nil {
		return fmt.Errorf("model %s: %w", modelID, err)
	}
	if ok {
		mc.UseModelName = inferred
	}
	return nil
}

func inferUseModelName(mc ModelConfig) (string, bool, error) {
	args, err := mc.SanitizedCommand()
	if err != nil {
		return "", false, fmt.Errorf("backend %s: sanitize command: %w", mc.Backend, err)
	}

	switch mc.Backend {
	case "mlx":
		value, ok := commandFlagValue(args, "--model", "-m")
		if !ok {
			return "", false, fmt.Errorf("backend mlx requires useModelName or a --model flag in cmd")
		}
		return value, true, nil
	case "vllm":
		if value, ok := commandFlagValue(args, "--served-model-name", "--served_model_name"); ok {
			return value, true, nil
		}
		value, ok := commandFlagValue(args, "--model")
		if !ok {
			return "", false, fmt.Errorf("backend vllm requires useModelName, --served-model-name, or --model in cmd")
		}
		return value, true, nil
	default:
		return "", false, nil
	}
}

func commandFlagValue(args []string, names ...string) (string, bool) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		for _, name := range names {
			if arg == name && i+1 < len(args) {
				return args[i+1], true
			}
			if strings.HasPrefix(arg, name+"=") {
				return strings.TrimPrefix(arg, name+"="), true
			}
		}
	}
	return "", false
}
