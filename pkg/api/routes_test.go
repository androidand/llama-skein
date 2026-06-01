package api

import (
	"encoding/json"
	"os"
	"testing"
)

func TestRouteConstantsExistInOpenAPIContract(t *testing.T) {
	raw, err := os.ReadFile("../../contracts/llama-skein.openapi.json")
	if err != nil {
		t.Fatalf("read contract: %v", err)
	}
	var spec struct {
		Paths map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("parse contract: %v", err)
	}
	for _, route := range []string{
		RouteHardware,
		RouteSystemVersion,
		RouteSystemCapabilities,
	} {
		if _, ok := spec.Paths[route]; !ok {
			t.Fatalf("route %q missing from OpenAPI contract", route)
		}
	}
}
