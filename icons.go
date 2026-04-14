package main

import (
	"fmt"
	"hash/fnv"
	"net/url"
	"strings"
	"sync"
)

// iconMap — mapping of service names to icons and colors
//
//nolint:gochecknoglobals
var iconMap = map[string]iconEntry{
	// Virtualization and infrastructure
	"proxmox":    {"simple-icons:proxmox", "#FDE8D0", "#E57000"},
	"vmware":     {"simple-icons:vmware", "#E3F2FD", "#607080"},
	"virtualbox": {"simple-icons:virtualbox", "#E3F2FD", "#184A84"},
	"hyper-v":    {"mdi:microsoft-hyper-v", "#E3F2FD", ""},

	// Network and DNS
	"adguard":      {"simple-icons:adguard", "#E2F5E6", "#68BC71"},
	"adguard home": {"simple-icons:adguard", "#E2F5E6", "#68BC71"},
	"роутер":       {"mdi:router-wireless", "#E3F2FD", ""},
	"router":       {"mdi:router-wireless", "#E3F2FD", ""},
	"google dns":   {"simple-icons:google", "#FCE4E4", ""},
	"1.1.1.1":      {"mdi:dns", "#FDE8D0", ""},
	"1.0.0.1":      {"mdi:dns", "#FDE8D0", ""},
	"1.1.1.1 dns":  {"mdi:dns", "#FDE8D0", ""},
	"1.0.0.1 dns":  {"mdi:dns", "#FDE8D0", ""},
	"cloudflare":   {"simple-icons:cloudflare", "#FDE8D0", "#F48120"},
	"opnsense":     {"simple-icons:opnsense", "#E3F2FD", "#D05C1D"},
	"pfsense":      {"simple-icons:pfsense", "#FCE4E4", "#212121"},
	"mikrotik":     {"simple-icons:mikrotik", "#FCE4E4", "#29333D"},
	"ubiquiti":     {"simple-icons:ubiquiti", "#E8E8FF", "#005FFF"},

	// Smart home
	"home assistant": {"simple-icons:homeassistant", "#E3F2FD", "#41BDF5"},
	"hass":           {"simple-icons:homeassistant", "#E3F2FD", "#41BDF5"},
	"homebridge":     {"simple-icons:homebridge", "#FCE4E4", "#491F5E"},
	"domoticz":       {"mdi:home-automation", "#E6F5E0", ""},
	"iobroker":       {"simple-icons:iobroker", "#D9EDF2", ""},

	// Containers and orchestration
	"docker":     {"simple-icons:docker", "#E3F2FD", "#2496ED"},
	"podman":     {"simple-icons:podman", "#E3F2FD", "#892CA0"},
	"kubernetes": {"simple-icons:kubernetes", "#D9EDF2", "#326CE5"},
	"k8s":        {"simple-icons:kubernetes", "#D9EDF2", "#326CE5"},
	"portainer":  {"simple-icons:portainer", "#E6F5E0", "#65BC40"},
	"rancher":    {"simple-icons:rancher", "#D9EDF2", "#009DDD"},

	// Monitoring and logs
	"grafana":     {"simple-icons:grafana", "#FDE8D0", "#F46800"},
	"prometheus":  {"simple-icons:prometheus", "#FCE4E4", "#E6522C"},
	"datadog":     {"simple-icons:datadog", "#FDE8D0", "#632CA6"},
	"uptime-kuma": {"simple-icons:uptime-kuma", "#E6F5E0", "#5CCE3B"},
	"zabbix":      {"simple-icons:zabbix", "#E3F2FD", "#C41E3A"},
	"netdata":     {"simple-icons:netdata", "#FCE4E4", "#00AB44"},

	// Web servers
	"nginx":   {"simple-icons:nginx", "#DFF5E6", "#009639"},
	"apache":  {"simple-icons:apache", "#FCE4E4", "#D22128"},
	"caddy":   {"simple-icons:caddy", "#FDE8D0", "#22313F"},
	"traefik": {"simple-icons:traefik", "#D9EDF2", "#24A5BE"},
	"haproxy": {"simple-icons:haproxy", "#FCE4E4", "#1064A8"},

	// Databases
	"mysql":         {"simple-icons:mysql", "#E3F2FD", "#4479A1"},
	"mariadb":       {"simple-icons:mariadb", "#E3F2FD", "#003545"},
	"postgres":      {"simple-icons:postgresql", "#E3F2FD", "#336791"},
	"postgresql":    {"simple-icons:postgresql", "#E3F2FD", "#336791"},
	"redis":         {"simple-icons:redis", "#FCE4E4", "#DC382D"},
	"mongodb":       {"simple-icons:mongodb", "#E6F5E0", "#47A248"},
	"elasticsearch": {"simple-icons:elasticsearch", "#D9EDF2", "#005571"},
	"influxdb":      {"simple-icons:influxdb", "#D9EDF2", "#22ADF6"},
	"sqlite":        {"simple-icons:sqlite", "#E3F2FD", "#003B57"},

	// Security
	"pihole":      {"simple-icons:pi-hole", "#FCE4E4", "#96060C"},
	"pi-hole":     {"simple-icons:pi-hole", "#FCE4E4", "#96060C"},
	"bitwarden":   {"simple-icons:bitwarden", "#DCE6F8", "#175DDC"},
	"vaultwarden": {"simple-icons:vaultwarden", "#E6F0FA", "#5B9FE5"},
	"wireguard":   {"simple-icons:wireguard", "#FCE4E4", "#88171A"},
	"tailscale":   {"simple-icons:tailscale", "#E8E8EE", "#24243A"},
	"authentik":   {"simple-icons:authentik", "#D9EDF2", "#FD4B2D"},
	"authelia":    {"simple-icons:authelia", "#E3F2FD", "#0047AB"},

	// Media and entertainment
	"plex":         {"simple-icons:plex", "#FDF3D0", "#EBAF00"},
	"jellyfin":     {"simple-icons:jellyfin", "#D9EDF2", "#00A4DC"},
	"emby":         {"simple-icons:emby", "#FDE8D0", "#52B54B"},
	"sonarr":       {"simple-icons:sonarr", "#E3F2FD", "#55B2E5"},
	"radarr":       {"simple-icons:radarr", "#FDE8D0", "#FF4E00"},
	"prowlarr":     {"simple-icons:prowlarr", "#FCE4E4", "#FF4E00"},
	"lidarr":       {"simple-icons:lidarr", "#FDE8D0", "#E03E3E"},
	"bazarr":       {"mdi:movie", "#E6F5E0", ""},
	"sabnzbd":      {"simple-icons:sabnzbd", "#FDF3D0", "#50A345"},
	"qbittorrent":  {"simple-icons:qbittorrent", "#D9EDF2", "#44A6EB"},
	"transmission": {"simple-icons:transmission", "#E6F5E0", "#D8283F"},
	"deluge":       {"simple-icons:deluge", "#E3F2FD", "#094D92"},
	"tautulli":     {"simple-icons:tautulli", "#FCE4E4", "#E5A244"},

	// Cloud storage and NAS
	"nextcloud": {"simple-icons:nextcloud", "#D9EDF2", "#0082C9"},
	"owncloud":  {"simple-icons:owncloud", "#D9EDF2", "#041E42"},
	"truenas":   {"simple-icons:truenas", "#D9EDF2", "#0095D5"},
	"synology":  {"simple-icons:synology", "#F0F0F0", "#B5B5B5"},
	"qnap":      {"simple-icons:qnap", "#F0F0F0", ""},
	"minio":     {"simple-icons:minio", "#FCE4E4", "#C72E49"},

	// CI/CD and development
	"github":    {"simple-icons:github", "#E3E3E3", "#181717"},
	"gitlab":    {"simple-icons:gitlab", "#FDE8D0", "#FC6D26"},
	"gitea":     {"simple-icons:gitea", "#E6F5E0", "#609926"},
	"jenkins":   {"simple-icons:jenkins", "#FCE4E4", "#D24939"},
	"sonarqube": {"simple-icons:sonarqube", "#D9EDF2", "#549DD1"},

	// Communications
	"telegram":   {"simple-icons:telegram", "#E3F2FD", "#26A5E4"},
	"discord":    {"simple-icons:discord", "#E3F2FD", "#5865F2"},
	"slack":      {"simple-icons:slack", "#E8E8EE", "#4A154B"},
	"mattermost": {"simple-icons:mattermost", "#D9EDF2", "#0058CC"},
	"matrix":     {"simple-icons:matrix", "#E6F5E0", "#000000"},
	"zulip":      {"simple-icons:zulip", "#E3F2FD", "#6494D8"},

	// Other
	"mqtt":     {"mdi:access-point", "#E6F5E0", ""},
	"zigbee":   {"mdi:zigbee", "#D9EDF2", ""},
	"zwave":    {"mdi:z-wave", "#FDF3D0", ""},
	"windows":  {"mdi:microsoft-windows", "#E3F2FD", ""},
	"linux":    {"mdi:linux", "#F0F0F0", ""},
	"firefox":  {"simple-icons:firefox", "#FDE8D0", "#FF7139"},
	"chrome":   {"simple-icons:googlechrome", "#FDE8D0", "#4285F4"},
	"vscode":   {"simple-icons:visualstudiocode", "#E3F2FD", "#007ACC"},
	"notion":   {"simple-icons:notion", "#F0F0F0", "#000000"},
	"obsidian": {"simple-icons:obsidian", "#F0F0F0", "#7C3AED"},
	"spotify":  {"simple-icons:spotify", "#E6F5E0", "#1DB954"},
	"youtube":  {"simple-icons:youtube", "#FCE4E4", "#FF0000"},
	"netflix":  {"simple-icons:netflix", "#FCE4E4", "#E50914"},
	"steam":    {"simple-icons:steam", "#F0F0F0", "#1B2838"},
}

var fallbackIcons = []string{
	"mdi:server", "mdi:web", "mdi:network",
	"mdi:application", "mdi:cloud", "mdi:lan",
}

var pastelColors = []string{
	"#E8E5FF", "#FCE4EC", "#E0F2F1", "#FFF3E0", "#EDE7F6",
	"#FFEBEE", "#E8F5E9", "#E3F2FD", "#FFF3E0", "#F1F8E9",
}

// iconMapKeys — cached keys for substring search (optimization)
var iconMapKeys []string

// iconCache stores resolved icon/color values to avoid repeated lookups
var (
	iconCache      = make(map[string]string) // name -> icon
	colorCache     = make(map[string]string) // name -> bgColor
	iconColorCache = make(map[string]string) // name -> iconColor
	cdnCache       = make(map[string]string) // name:icon -> cdnURL
	cacheMu        sync.RWMutex
)

func init() {
	// Cache keys for iteration
	iconMapKeys = make([]string, 0, len(iconMap))
	for k := range iconMap {
		iconMapKeys = append(iconMapKeys, k)
	}
}

// cacheGet retrieves a value from the icon cache
func cacheGet(cache map[string]string, key string) (string, bool) {
	val, ok := cache[key]
	return val, ok
}

// cacheSet stores a value in the icon cache
func cacheSet(cache map[string]string, key, value string) {
	// Limit cache size — clear entirely if at capacity
	if len(cache) >= 500 {
		clear(cache)
	}
	cache[key] = value
}

func generatePastelColor(s string) string {
	h := fnv.New32a()
	h.Write([]byte(s))
	return pastelColors[h.Sum32()%uint32(len(pastelColors))]
}

func resolveIcon(name, icon string) string {
	if strings.TrimSpace(icon) != "" {
		return icon
	}
	key := strings.ToLower(strings.TrimSpace(name))

	cacheMu.RLock()
	if val, ok := cacheGet(iconCache, key); ok {
		cacheMu.RUnlock()
		return val
	}
	cacheMu.RUnlock()

	result := resolveIconUncached(key)

	cacheMu.Lock()
	cacheSet(iconCache, key, result)
	cacheMu.Unlock()

	return result
}

func resolveIconUncached(lower string) string {
	if e, ok := iconMap[lower]; ok {
		return e.Icon
	}
	for _, key := range iconMapKeys {
		if strings.Contains(lower, key) || strings.Contains(key, lower) {
			return iconMap[key].Icon
		}
	}
	h := fnv.New32a()
	h.Write([]byte(lower))
	return fallbackIcons[h.Sum32()%uint32(len(fallbackIcons))]
}

func resolveColor(name string) string {
	key := strings.ToLower(strings.TrimSpace(name))

	cacheMu.RLock()
	if val, ok := cacheGet(colorCache, key); ok {
		cacheMu.RUnlock()
		return val
	}
	cacheMu.RUnlock()

	result := resolveColorUncached(key)

	cacheMu.Lock()
	cacheSet(colorCache, key, result)
	cacheMu.Unlock()

	return result
}

func resolveColorUncached(lower string) string {
	if e, ok := iconMap[lower]; ok && e.BgColor != "" {
		return e.BgColor
	}
	for _, key := range iconMapKeys {
		e := iconMap[key]
		if e.BgColor == "" {
			continue
		}
		if strings.Contains(lower, key) || strings.Contains(key, lower) {
			return e.BgColor
		}
	}
	return generatePastelColor(lower)
}

func resolveIconColor(name string) string {
	key := strings.ToLower(strings.TrimSpace(name))

	cacheMu.RLock()
	if val, ok := cacheGet(iconColorCache, key); ok {
		cacheMu.RUnlock()
		return val
	}
	cacheMu.RUnlock()

	result := resolveIconColorUncached(key)

	cacheMu.Lock()
	cacheSet(iconColorCache, key, result)
	cacheMu.Unlock()

	return result
}

func resolveIconColorUncached(lower string) string {
	if e, ok := iconMap[lower]; ok && e.IconColor != "" {
		return e.IconColor
	}
	for _, key := range iconMapKeys {
		e := iconMap[key]
		if e.IconColor == "" {
			continue
		}
		if strings.Contains(lower, key) || strings.Contains(key, lower) {
			return e.IconColor
		}
	}
	return ""
}

func resolveIconCDN(name, explicitIcon string) string {
	cacheKey := name + ":" + explicitIcon

	cacheMu.RLock()
	if val, ok := cacheGet(cdnCache, cacheKey); ok {
		cacheMu.RUnlock()
		return val
	}
	cacheMu.RUnlock()

	result := resolveIconCDNUncached(name, explicitIcon)

	cacheMu.Lock()
	cacheSet(cdnCache, cacheKey, result)
	cacheMu.Unlock()

	return result
}

func resolveIconCDNUncached(name, explicitIcon string) string {
	icon := resolveIcon(name, explicitIcon)

	if strings.HasPrefix(icon, "http://") || strings.HasPrefix(icon, "https://") {
		return icon
	}

	parts := strings.SplitN(icon, ":", 2)
	if len(parts) != 2 {
		return generateFallbackSVG(name)
	}

	brandColor := resolveIconColor(name)
	base := fmt.Sprintf("https://api.iconify.design/%s/%s.svg",
		url.PathEscape(parts[0]), url.PathEscape(parts[1]))

	if brandColor == "" {
		return base
	}
	return base + "?color=" + url.QueryEscape(brandColor)
}

func generateFallbackSVG(name string) string {
	letter := "?"
	if len(name) > 0 {
		letter = strings.ToUpper(name[:1])
	}
	escaped := url.QueryEscape(fmt.Sprintf(
		`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 40 40">`+
			`<rect width="40" height="40" rx="8" fill="#E3F2FD"/>`+
			`<text x="20" y="28" text-anchor="middle" font-size="22" font-family="Arial,sans-serif" fill="#1f2328">%s</text></svg>`, letter))
	return "data:image/svg+xml;charset=utf-8," + escaped
}
