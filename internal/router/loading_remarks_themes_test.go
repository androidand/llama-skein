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
	// wantMin: minimum acceptable number of remarks for this theme.
	// wantOne: expected first remark (identifies the correct theme list was returned).
	tests := []struct {
		name    string
		theme   LoadingTheme
		wantMin int
		wantOne string
	}{
		{
			name:    "default",
			theme:   LoadingThemeDefault,
			wantMin: 20,
			wantOne: loadingRemarks[0],
		},
		{
			name:    "vault-boy",
			theme:   LoadingThemeVaultBoy,
			wantMin: 20,
			wantOne: "Please stand by",
		},
		{
			name:    "knight-rider",
			theme:   LoadingThemeKnightRider,
			wantMin: 20,
			wantOne: "Initializing KITT",
		},
		{
			name:    "skein",
			theme:   LoadingThemeSkein,
			wantMin: 20,
			wantOne: "Untangling the skein",
		},
		{
			name:    "unknown theme falls back to default",
			theme:   "nonexistent",
			wantMin: 20,
			wantOne: loadingRemarks[0],
		},
		{
			name:    "empty theme falls back to default",
			theme:   "",
			wantMin: 20,
			wantOne: loadingRemarks[0],
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			remarks := resolveThemeRemarks(tt.theme)
			if len(remarks) < tt.wantMin {
				t.Errorf("expected at least %d remarks, got %d", tt.wantMin, len(remarks))
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
