package router

import "testing"

func TestLoadingThemeConstants(t *testing.T) {
	tests := []struct {
		theme  LoadingTheme
		want   string
	}{
		{LoadingThemeDefault, "default"},
		{LoadingThemeVaultBoy, "vault-boy"},
		{LoadingThemeKnightRider, "knight-rider"},
		{LoadingThemeSkein, "skein"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if string(tt.theme) != tt.want {
				t.Errorf("LoadingTheme %s = %q, want %q", tt.want, tt.theme, tt.want)
			}
		})
	}
}

func TestResolveThemeRemarks(t *testing.T) {
	tests := []struct {
		name     string
		theme    LoadingTheme
		wantLen  int
		wantOne  string
		wantAll  int // expected total slice length
	}{
		{
			name:    "default",
			theme:   LoadingThemeDefault,
			wantLen: 20,
			wantOne: "Loading model",
			wantAll: 20,
		},
		{
			name:    "vault-boy",
			theme:   LoadingThemeVaultBoy,
			wantLen: 28,
			wantOne: "Please stand by",
			wantAll: 28,
		},
		{
			name:    "knight-rider",
			theme:   LoadingThemeKnightRider,
			wantLen: 26,
			wantOne: "Initializing KITT",
			wantAll: 26,
		},
		{
			name:    "skein",
			theme:   LoadingThemeSkein,
			wantLen: 25,
			wantOne: "Untangling the skein",
			wantAll: 25,
		},
		{
			name:    "unknown theme falls back to default",
			theme:   "nonexistent",
			wantLen: 20,
			wantAll: 20,
		},
		{
			name:    "empty theme falls back to default",
			theme:   "",
			wantLen: 20,
			wantAll: 20,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			remarks := resolveThemeRemarks(tt.theme)
			if len(remarks) != tt.wantAll {
				t.Errorf("expected %d remarks, got %d", tt.wantAll, len(remarks))
			}
			if len(remarks) == 0 {
				return
			}
			if tt.wantOne != "" && remarks[0] != tt.wantOne {
				t.Errorf("first remark: want %q, got %q", tt.wantOne, remarks[0])
			}
		})
	}
}

func TestResolveThemeRemarks_DifferentThemesReturnDifferentSlices(t *testing.T) {
	defaults := resolveThemeRemarks(LoadingThemeDefault)
	vault := resolveThemeRemarks(LoadingThemeVaultBoy)
	knight := resolveThemeRemarks(LoadingThemeKnightRider)
	skein := resolveThemeRemarks(LoadingThemeSkein)

	if len(defaults) == len(vault) || len(defaults) == len(knight) || len(defaults) == len(skein) {
		t.Errorf("expected different slice lengths, got all %d", len(defaults))
	}

	// All themes should have at least 20 remarks
	for name, slice := range map[string][]string{
		"default": defaults,
		"vault":   vault,
		"knight":  knight,
		"skein":   skein,
	} {
		if len(slice) < 20 {
			t.Errorf("%s: expected at least 20 remarks, got %d", name, len(slice))
		}
	}
}

func TestLoadingRemarkThemesMap(t *testing.T) {
	if len(loadingRemarkThemes) != 4 {
		t.Errorf("expected 4 themes, got %d", len(loadingRemarkThemes))
	}

	// Verify all constant themes are registered
	for theme := range loadingRemarkThemes {
		if resolveThemeRemarks(theme) == nil {
			t.Errorf("theme %q not found in resolveThemeRemarks", theme)
		}
	}
}
