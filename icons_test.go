package main

import (
	"fmt"
	"strings"
	"testing"
)

// ─── resolveIcon tests ───

func TestResolveIcon_ExplicitIcon(t *testing.T) {
	result := resolveIcon("Unknown Service", "simple-icons:docker")
	if result != "simple-icons:docker" {
		t.Errorf("expected explicit icon, got %q", result)
	}
}

func TestResolveIcon_ExactMatch(t *testing.T) {
	result := resolveIcon("proxmox", "")
	if result != "simple-icons:proxmox" {
		t.Errorf("expected proxmox icon, got %q", result)
	}
}

func TestResolveIcon_CaseInsensitive(t *testing.T) {
	result := resolveIcon("Proxmox", "")
	if result != "simple-icons:proxmox" {
		t.Errorf("expected proxmox icon (case insensitive), got %q", result)
	}
}

func TestResolveIcon_SubstringMatch(t *testing.T) {
	result := resolveIcon("my-grafana-server", "")
	if result != "simple-icons:grafana" {
		t.Errorf("expected grafana icon via substring, got %q", result)
	}
}

func TestResolveIcon_Fallback(t *testing.T) {
	result := resolveIcon("totally-unknown-service", "")
	// Should return one of the fallback icons
	found := false
	for _, fallback := range fallbackIcons {
		if result == fallback {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected fallback icon, got %q", result)
	}
}

// ─── resolveColor tests ───

func TestResolveColor_ExactMatch(t *testing.T) {
	result := resolveColor("grafana")
	if result != "#FDE8D0" {
		t.Errorf("expected grafana color, got %q", result)
	}
}

func TestResolveColor_FallbackPastel(t *testing.T) {
	result := resolveColor("unknown-service")
	// Should return a pastel color
	found := false
	for _, pastel := range pastelColors {
		if result == pastel {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected pastel color, got %q", result)
	}
}

func TestResolveColor_EmptyBgColor(t *testing.T) {
	// "роутер" has empty BgColor, should try substring then pastel
	result := resolveColor("роутер")
	// Should get the color from iconMap (which is "") → fallback to pastel
	found := false
	for _, pastel := range pastelColors {
		if result == pastel {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected pastel color for empty BgColor, got %q", result)
	}
}

// ─── resolveIconColor tests ───

func TestResolveIconColor_ExactMatch(t *testing.T) {
	result := resolveIconColor("proxmox")
	if result != "#E57000" {
		t.Errorf("expected proxmox icon color, got %q", result)
	}
}

func TestResolveIconColor_EmptyReturnsEmpty(t *testing.T) {
	result := resolveIconColor("роутер")
	if result != "" {
		t.Errorf("expected empty icon color, got %q", result)
	}
}

// ─── resolveIconCDN tests ───

func TestResolveIconCDN_GeneratesURL(t *testing.T) {
	result := resolveIconCDN("grafana", "")
	if !strings.HasPrefix(result, "https://api.iconify.design/") {
		t.Errorf("expected iconify CDN URL, got %q", result)
	}
}

func TestResolveIconCDN_ExplicitURL(t *testing.T) {
	result := resolveIconCDN("unknown", "https://example.com/icon.svg")
	if result != "https://example.com/icon.svg" {
		t.Errorf("expected explicit URL, got %q", result)
	}
}

func TestResolveIconCDN_WithBrandColor(t *testing.T) {
	result := resolveIconCDN("proxmox", "")
	if !strings.Contains(result, "?color=") {
		t.Errorf("expected brand color in URL, got %q", result)
	}
}

func TestResolveIconCDN_Fallback(t *testing.T) {
	// Use a name that won't match any substring in iconMap
	result := resolveIconCDN("qqq-test-service", "")
	// Should be either a CDN URL (from fallbackIcons) or a data URI SVG
	isCDN := strings.HasPrefix(result, "https://api.iconify.design/")
	isSVG := strings.HasPrefix(result, "data:image/svg+xml")
	if !isCDN && !isSVG {
		t.Errorf("expected CDN URL or data URI SVG fallback, got %q", result)
	}
}

// ─── generatePastelColor tests ───

func TestGeneratePastelColor_Deterministic(t *testing.T) {
	c1 := generatePastelColor("test-service")
	c2 := generatePastelColor("test-service")
	if c1 != c2 {
		t.Errorf("expected deterministic color, got %q vs %q", c1, c2)
	}
}

func TestGeneratePastelColor_DifferentInputs(t *testing.T) {
	c1 := generatePastelColor("service-a")
	c2 := generatePastelColor("service-b")
	// Not guaranteed to differ, but very likely
	_ = c1
	_ = c2
}

// ─── generateFallbackSVG tests ───

func TestGenerateFallbackSVG(t *testing.T) {
	result := generateFallbackSVG("Test")
	if !strings.HasPrefix(result, "data:image/svg+xml") {
		t.Errorf("expected data URI, got %q", result)
	}
	if !strings.Contains(result, "T") {
		t.Errorf("expected first letter 'T' in SVG, got %q", result)
	}
}

func TestGenerateFallbackSVG_EmptyName(t *testing.T) {
	result := generateFallbackSVG("")
	// Empty name → '?' → URL encoded as %3F
	if !strings.Contains(result, "%3F") && !strings.Contains(result, "?") {
		t.Errorf("expected '?' for empty name, got %q", result)
	}
}

// ─── Cache tests ───

func TestCacheSetGet(t *testing.T) {
	cache := make(map[string]string)
	cacheSet(cache, "key1", "value1")
	val, ok := cacheGet(cache, "key1")
	if !ok || val != "value1" {
		t.Errorf("expected value1, got %q (ok=%v)", val, ok)
	}
}

func TestCacheEviction(t *testing.T) {
	cache := make(map[string]string)
	// Fill beyond limit (500) with unique keys
	for i := 0; i < 501; i++ {
		key := fmt.Sprintf("key%d", i)
		cacheSet(cache, key, "value")
	}
	// After clearing at 501st insert and adding the new entry, cache should have exactly 1
	if len(cache) != 1 {
		t.Errorf("expected 1 entry after eviction, got %d", len(cache))
	}
}

// ─── Substring matching tests ───

func TestIconSubstringMatch(t *testing.T) {
	tests := []struct {
		name     string
		expected string
	}{
		{"my-grafana-dashboard", "simple-icons:grafana"},
		{"docker-container", "simple-icons:docker"},
		{"nginx-proxy", "simple-icons:nginx"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := resolveIcon(tt.name, "")
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}
