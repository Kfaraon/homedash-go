package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func writeIconsConfig(t *testing.T, mutate func(map[string]any)) string {
	t.Helper()

	raw, err := os.ReadFile("data/icons.json")
	if err != nil {
		t.Fatalf("read icons: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal icons: %v", err)
	}
	if mutate != nil {
		mutate(m)
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "icons.json")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestIconsFallbackSVGFromJSON(t *testing.T) {
	path := writeIconsConfig(t, func(m map[string]any) {
		m["defaults"].(map[string]any)["fallback_svg"] = map[string]any{
			"view_box":    "0 0 64 64",
			"bg_color":    "#112233",
			"text_color":  "#ffffff",
			"font_size":   30,
			"font_family": "Courier",
		}
	})
	t.Setenv("ICONS_CONFIG_PATH", path)

	r := NewIconResolver()
	if r.svgViewBox != "0 0 64 64" {
		t.Fatalf("viewBox = %q, want 0 0 64 64", r.svgViewBox)
	}
	if r.svgFontSize != 30 {
		t.Fatalf("fontSize = %d, want 30", r.svgFontSize)
	}
	if r.svgBgColor != "112233" {
		t.Fatalf("bg = %q", r.svgBgColor)
	}
}

func TestIconsFallbackSVGCyrillic(t *testing.T) {
	t.Setenv("ICONS_CONFIG_PATH", "data/icons.json")
	r := NewIconResolver()

	uri := r.getCachedFallbackSVG("Роутер")
	const prefix = "data:image/svg+xml;charset=utf-8;base64,"
	if !strings.HasPrefix(uri, prefix) {
		t.Fatalf("unexpected fallback URI: %q", uri)
	}
	svg, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(uri, prefix))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !utf8.Valid(svg) {
		t.Fatalf("fallback SVG is not valid UTF-8: %q", svg)
	}
	if !strings.Contains(string(svg), "Р") {
		t.Fatalf("fallback SVG does not contain the Cyrillic letter: %q", svg)
	}
}

func TestIconsExplicitIconWins(t *testing.T) {
	t.Setenv("ICONS_CONFIG_PATH", "data/icons.json")
	r := NewIconResolver()

	if got := r.ResolveIcon("whatever", "mdi:server"); got != "mdi:server" {
		t.Fatalf("explicit icon ignored: %q", got)
	}
	if got := r.ResolveIconColor("grafana"); got == "" {
		t.Fatal("expected a color for grafana")
	}
}

// Регрессия: раньше при отсутствии лексического совпадения запись с ненулевым
// priority (Home Assistant) ошибочно выбиралась для любого имени.
func TestIconsUnknownNameIsNotHomeAssistant(t *testing.T) {
	t.Setenv("ICONS_CONFIG_PATH", "data/icons.json")
	r := NewIconResolver()

	for _, name := range []string{"opencode", "zzz-unknown-service", "совершенно новый сервис"} {
		if got := r.ResolveIcon(name, ""); got == "simple-icons:homeassistant" {
			t.Fatalf("%q resolved to Home Assistant icon", name)
		}
	}
}

func TestIconsOpenCodeCatalogEntry(t *testing.T) {
	t.Setenv("ICONS_CONFIG_PATH", "data/icons.json")
	r := NewIconResolver()

	for _, name := range []string{"opencode", "OpenCode", "open code"} {
		got := r.ResolveIcon(name, "")
		if got != "simple-icons:opencode" {
			t.Fatalf("%q -> %q, want simple-icons:opencode", name, got)
		}
	}
}

// Неизвестный сервис должен получить кандидатов из iconify, а заглушку — последней.
func TestIconsCandidatesForUnknownService(t *testing.T) {
	t.Setenv("ICONS_CONFIG_PATH", "data/icons.json")
	r := NewIconResolver()

	cands := r.IconCandidates("Brand New Tool", "")
	if len(cands) < 2 {
		t.Fatalf("expected iconify candidates, got %v", cands)
	}
	if !strings.Contains(cands[0], "api.iconify.design/selfhst/brandnewtool.svg") {
		t.Fatalf("first candidate = %q", cands[0])
	}
	// Дефисный вариант тоже должен присутствовать (selfhst использует "rocket-chat").
	var hasHyphen bool
	for _, c := range cands {
		if strings.Contains(c, "selfhst/brand-new-tool.svg") {
			hasHyphen = true
		}
	}
	if !hasHyphen {
		t.Fatalf("expected hyphenated slug candidate, got %v", cands)
	}
	last := cands[len(cands)-1]
	if !strings.HasPrefix(last, "data:") && !strings.Contains(last, "mdi/") {
		t.Fatalf("last candidate should be a fallback, got %q", last)
	}
}

func TestIconsCandidatesCatalogEntrySingle(t *testing.T) {
	t.Setenv("ICONS_CONFIG_PATH", "data/icons.json")
	r := NewIconResolver()

	cands := r.IconCandidates("nginx", "")
	if len(cands) != 1 {
		t.Fatalf("known service should have a single candidate, got %v", cands)
	}
}

func TestIconsCandidatesExplicitSingle(t *testing.T) {
	t.Setenv("ICONS_CONFIG_PATH", "data/icons.json")
	r := NewIconResolver()

	cands := r.IconCandidates("whatever", "simple-icons:foo")
	if len(cands) != 1 || !strings.Contains(cands[0], "simple-icons/foo.svg") {
		t.Fatalf("explicit icon must yield a single candidate, got %v", cands)
	}
}

// lookup_enabled=false в icons.json отключает подбор из iconify.
func TestIconsCandidatesLookupDisabledFromConfig(t *testing.T) {
	path := writeIconsConfig(t, func(m map[string]any) {
		m["defaults"].(map[string]any)["lookup_enabled"] = false
	})
	t.Setenv("ICONS_CONFIG_PATH", path)

	r := NewIconResolver()
	cands := r.IconCandidates("Brand New Tool", "")
	if len(cands) != 1 {
		t.Fatalf("lookup disabled should yield only the fallback, got %v", cands)
	}
}

// lookup_collections из icons.json задаёт порядок коллекций iconify.
func TestIconsCandidatesLookupCollectionsFromConfig(t *testing.T) {
	path := writeIconsConfig(t, func(m map[string]any) {
		m["defaults"].(map[string]any)["lookup_collections"] = []string{"logos"}
	})
	t.Setenv("ICONS_CONFIG_PATH", path)

	r := NewIconResolver()
	cands := r.IconCandidates("Brand New Tool", "")
	if len(cands) < 2 || !strings.Contains(cands[0], "api.iconify.design/logos/brandnewtool.svg") {
		t.Fatalf("expected logos candidate first, got %v", cands)
	}
}
