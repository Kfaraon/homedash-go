package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log/slog"
	"math"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Category описывает fallback-поведение для категории сервисов.
type Category struct {
	FallbackIcons []string `json:"fallback_icons"`
	BgColor       string   `json:"bg_color"`
	IconColor     string   `json:"icon_color"`
	Keywords      []string `json:"keywords"`
}

// iconEntry представляет один загруженный сервис со всей метаинформацией.
type iconEntry struct {
	Key         string
	Icon        string
	BgColor     string
	IconColor   string
	Category    string
	Priority    int
	SearchTerms []string
	Tokens      []string // все токены (ключ + search terms)
}

// IconResolver отвечает за поиск иконок, цветов и CDN-ссылок.
type IconResolver struct {
	entries []*iconEntry
	iconMap map[string]*iconEntry // точный ключ -> entry
	aliases map[string]string     // алиас -> ключ
	tokenDF map[string]float64    // IDF вес токенов

	cache         sync.Map
	categoryCache sync.Map
	cdnCache      sync.Map
	svgCache      sync.Map

	categories       map[string]*Category
	sortedCategories []string

	cdnTemplate  string
	normRegex    *regexp.Regexp
	normReplacer *strings.Replacer

	pastelSat float64
	pastelVal float64
	defaultBg string

	fallbackIcon  string
	svgViewBox    string
	svgBgColor    string
	svgTextColor  string
	svgFontSize   int
	svgFontFamily string
}

// NewIconResolver загружает конфиг из JSON и строит индексы.
func NewIconResolver() *IconResolver {
	r := &IconResolver{
		iconMap:    make(map[string]*iconEntry),
		aliases:    make(map[string]string),
		categories: make(map[string]*Category),

		cdnTemplate:  "https://api.iconify.design/{collection}/{icon}.svg?color={color}",
		pastelSat:    0.3,
		pastelVal:    0.9,
		defaultBg:    "#E3F2FD",
		fallbackIcon: "mdi:server",

		normRegex:    regexp.MustCompile(`[^a-zа-яё0-9 ]+`),
		normReplacer: strings.NewReplacer("-", " ", "_", " ", ".", " "),

		svgViewBox:    "0 0 40 40",
		svgBgColor:    "#E3F2FD",
		svgTextColor:  "#1f2328",
		svgFontSize:   22,
		svgFontFamily: "Arial,sans-serif",
	}

	path := os.Getenv("ICONS_CONFIG_PATH")
	if path == "" {
		path = "data/icons.json"
	}

	if err := r.loadIconsFromJSON(path); err != nil {
		slog.Warn("Falling back to default icons", "error", err, "path", path)
		r.initDefaultCategories()
	}

	// убираем # из дефолтных цветов
	r.svgBgColor = strings.TrimPrefix(r.svgBgColor, "#")
	r.svgTextColor = strings.TrimPrefix(r.svgTextColor, "#")
	r.defaultBg = strings.TrimPrefix(r.defaultBg, "#")

	r.buildIndices()
	r.prepareSortedCategories()
	return r
}

// sanitizeJSON исправляет мелкие проблемы форматирования в JSON.
func sanitizeJSON(data []byte) []byte {
	s := string(data)
	s = regexp.MustCompile(`"([^"]+?)\s*"\s*:`).ReplaceAllString(s, `"${1}":`)
	s = regexp.MustCompile(`:\s*"([^"]*?)\s*"(,|\s*})`).ReplaceAllString(s, `: "${1}"$2`)
	s = strings.NewReplacer(
		`"saturation":`, `"sat":`,
		`"brightness":`, `"bright":`,
		`"default_bg":`, `"bg":`,
		`"replace_chars":`, `"chars":`,
	).Replace(s)
	return []byte(s)
}

// loadIconsFromJSON парсит JSON-конфиг в структуры.
func (r *IconResolver) loadIconsFromJSON(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	data = sanitizeJSON(data)

	var cfg struct {
		Icons []struct {
			Key      string   `json:"key"`
			Icon     string   `json:"icon"`
			Bg       string   `json:"bg"`
			Color    string   `json:"color"`
			Category string   `json:"category"`
			Search   []string `json:"search"`
			Priority int      `json:"priority"`
		} `json:"icons"`
		Aliases    map[string]string    `json:"aliases"`
		Categories map[string]*Category `json:"categories"`
		Defaults   struct {
			CDNTemplate  string `json:"cdn_template"`
			FallbackIcon string `json:"fallback_icon"`
			Pastel       struct {
				Sat, Bright float64
				Bg          string
			} `json:"pastel_colors"`
			SVG struct {
				ViewBox, Bg, Text, FontFamily string
				Size                          int
			} `json:"fallback_svg"`
		} `json:"defaults"`
		Norm struct {
			Regex string
			Chars []string
		} `json:"normalization"`
	}

	if err := json.Unmarshal(data, &cfg); err != nil {
		return err
	}

	// Алиасы
	r.aliases = make(map[string]string, len(cfg.Aliases))
	for k, v := range cfg.Aliases {
		r.aliases[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}

	// Элементы
	r.entries = make([]*iconEntry, 0, len(cfg.Icons))
	for _, ic := range cfg.Icons {
		key := strings.TrimSpace(ic.Key)
		entry := &iconEntry{
			Key:         key,
			Icon:        strings.TrimSpace(ic.Icon),
			BgColor:     strings.TrimSpace(ic.Bg),
			IconColor:   strings.TrimSpace(ic.Color),
			Category:    strings.TrimSpace(ic.Category),
			Priority:    ic.Priority,
			SearchTerms: ic.Search,
		}
		r.iconMap[key] = entry
		r.entries = append(r.entries, entry)
	}

	// Категории
	if len(cfg.Categories) > 0 {
		r.categories = make(map[string]*Category, len(cfg.Categories))
		for k, v := range cfg.Categories {
			r.categories[strings.TrimSpace(k)] = v
		}
	}

	// Настройки по умолчанию
	if c := strings.TrimSpace(cfg.Defaults.CDNTemplate); c != "" {
		r.cdnTemplate = c
	}
	if c := strings.TrimSpace(cfg.Defaults.FallbackIcon); c != "" {
		r.fallbackIcon = c
	}
	if cfg.Defaults.Pastel.Sat > 0 {
		r.pastelSat = cfg.Defaults.Pastel.Sat
	}
	if cfg.Defaults.Pastel.Bright > 0 {
		r.pastelVal = cfg.Defaults.Pastel.Bright
	}
	if c := strings.TrimSpace(cfg.Defaults.Pastel.Bg); c != "" {
		r.defaultBg = c
	}
	if c := strings.TrimSpace(cfg.Defaults.SVG.ViewBox); c != "" {
		r.svgViewBox = c
	}
	if c := strings.TrimSpace(cfg.Defaults.SVG.Bg); c != "" {
		r.svgBgColor = c
	}
	if c := strings.TrimSpace(cfg.Defaults.SVG.Text); c != "" {
		r.svgTextColor = c
	}
	if cfg.Defaults.SVG.Size > 0 {
		r.svgFontSize = cfg.Defaults.SVG.Size
	}
	if c := strings.TrimSpace(cfg.Defaults.SVG.FontFamily); c != "" {
		r.svgFontFamily = c
	}
	if c := strings.TrimSpace(cfg.Norm.Regex); c != "" {
		if re, err := regexp.Compile(c); err == nil {
			r.normRegex = re
		}
	}
	if len(cfg.Norm.Chars) > 0 {
		args := make([]string, len(cfg.Norm.Chars)*2)
		for i, ch := range cfg.Norm.Chars {
			args[i*2] = ch
			args[i*2+1] = " "
		}
		r.normReplacer = strings.NewReplacer(args...)
	}
	return nil
}

// initDefaultCategories заполняет категории при ошибке загрузки JSON.
func (r *IconResolver) initDefaultCategories() {
	defs := map[string]Category{
		"db":             {FallbackIcons: []string{"simple-icons:postgresql", "simple-icons:mysql", "mdi:database"}, BgColor: "#E3F2FD", IconColor: "#336791", Keywords: []string{"database", "sql", "db"}},
		"web":            {FallbackIcons: []string{"mdi:web", "mdi:server", "simple-icons:nginx"}, BgColor: "#DFF5E6", IconColor: "#009639", Keywords: []string{"web", "http", "nginx", "apache"}},
		"network":        {FallbackIcons: []string{"mdi:router-wireless", "mdi:lan", "mdi:cloud"}, BgColor: "#E3F2FD", IconColor: "#607080", Keywords: []string{"network", "router", "firewall"}},
		"dns":            {FallbackIcons: []string{"mdi:dns", "mdi:server-network", "simple-icons:cloudflare"}, BgColor: "#FDE8D0", IconColor: "#F48120", Keywords: []string{"dns"}},
		"security":       {FallbackIcons: []string{"mdi:shield", "mdi:lock", "simple-icons:pi-hole"}, BgColor: "#FCE4E4", IconColor: "#96060C", Keywords: []string{"security", "vpn", "wireguard"}},
		"media":          {FallbackIcons: []string{"mdi:video", "mdi:music", "mdi:television"}, BgColor: "#FDF3D0", IconColor: "#EBAF00", Keywords: []string{"media", "video", "music", "stream"}},
		"storage":        {FallbackIcons: []string{"mdi:harddisk", "mdi:nas", "mdi:cloud-outline"}, BgColor: "#D9EDF2", IconColor: "#0082C9", Keywords: []string{"storage", "nas", "cloud"}},
		"container":      {FallbackIcons: []string{"mdi:docker", "mdi:cube-outline", "mdi:package-variant"}, BgColor: "#E3F2FD", IconColor: "#2496ED", Keywords: []string{"docker", "container"}},
		"orchestration":  {FallbackIcons: []string{"mdi:kubernetes", "mdi:orbit", "mdi:layers"}, BgColor: "#D9EDF2", IconColor: "#326CE5", Keywords: []string{"kubernetes", "k8s"}},
		"monitoring":     {FallbackIcons: []string{"mdi:chart-line", "mdi:eye", "mdi:gauge"}, BgColor: "#FDE8D0", IconColor: "#F46800", Keywords: []string{"monitoring", "metrics", "grafana"}},
		"smarthome":      {FallbackIcons: []string{"mdi:home-automation", "mdi:lightbulb", "mdi:home"}, BgColor: "#E3F2FD", IconColor: "#41BDF5", Keywords: []string{"smarthome", "home automation"}},
		"chat":           {FallbackIcons: []string{"mdi:message", "mdi:chat", "mdi:account-group"}, BgColor: "#E3F2FD", IconColor: "#26A5E4", Keywords: []string{"chat", "message", "telegram"}},
		"dev":            {FallbackIcons: []string{"mdi:code-braces", "mdi:git", "mdi:terminal"}, BgColor: "#E6F5E0", IconColor: "#609926", Keywords: []string{"dev", "git", "code"}},
		"editor":         {FallbackIcons: []string{"mdi:file-code", "mdi:pencil", "mdi:note-text"}, BgColor: "#F0F0F0", IconColor: "#7C3AED", Keywords: []string{"editor", "text"}},
		"virtualization": {FallbackIcons: []string{"mdi:server", "mdi:monitor-multiple", "mdi:cpu-64-bit"}, BgColor: "#E3F2FD", IconColor: "#E57000", Keywords: []string{"virtual", "vm", "proxmox"}},
		"iot":            {FallbackIcons: []string{"mdi:access-point", "mdi:wifi", "mdi:bluetooth"}, BgColor: "#E6F5E0", IconColor: "", Keywords: []string{"iot", "esp", "arduino", "raspberry"}},
		"os":             {FallbackIcons: []string{"mdi:desktop-classic", "mdi:linux", "mdi:microsoft-windows"}, BgColor: "#F0F0F0", IconColor: "", Keywords: []string{"os", "linux", "windows"}},
		"browser":        {FallbackIcons: []string{"mdi:web", "mdi:earth", "mdi:application"}, BgColor: "#FDE8D0", IconColor: "", Keywords: []string{"browser", "chrome", "firefox"}},
		"gaming":         {FallbackIcons: []string{"mdi:gamepad", "mdi:joystick", "mdi:television"}, BgColor: "#F0F0F0", IconColor: "", Keywords: []string{"game", "steam", "twitch"}},
		"dashboard":      {FallbackIcons: []string{"mdi:view-dashboard", "mdi:application", "mdi:grid"}, BgColor: "#E6F5E0", IconColor: "#4CAF50", Keywords: []string{"dashboard", "homepage"}},
		"social":         {FallbackIcons: []string{"mdi:account-group", "mdi:web", "mdi:share"}, BgColor: "#E3F2FD", IconColor: "#26A5E4", Keywords: []string{"social", "twitter", "facebook"}},
		"default":        {FallbackIcons: []string{"mdi:server", "mdi:application", "mdi:help-circle"}, BgColor: "", IconColor: "", Keywords: []string{}},
	}
	for k, v := range defs {
		c := v
		r.categories[k] = &c
	}
}

// buildIndices строит инвертированный индекс и вычисляет IDF для токенов.
func (r *IconResolver) buildIndices() {
	docCount := float64(len(r.entries))
	tokenDF := make(map[string]int)

	for _, e := range r.entries {
		tokenSet := make(map[string]struct{})
		addTokens := func(s string) {
			for _, t := range strings.Fields(s) {
				tokenSet[t] = struct{}{}
			}
		}
		addTokens(r.normalizeName(e.Key))
		for _, st := range e.SearchTerms {
			addTokens(r.normalizeName(st))
		}
		e.Tokens = make([]string, 0, len(tokenSet))
		for t := range tokenSet {
			e.Tokens = append(e.Tokens, t)
			tokenDF[t]++
		}
	}

	r.tokenDF = make(map[string]float64, len(tokenDF))
	for t, df := range tokenDF {
		if df > 0 {
			r.tokenDF[t] = math.Log(docCount / float64(df))
		} else {
			r.tokenDF[t] = 0
		}
	}
}

// prepareSortedCategories готовит список категорий, отсортированный по длине ключа (для детекции).
func (r *IconResolver) prepareSortedCategories() {
	keys := make([]string, 0, len(r.categories))
	for k := range r.categories {
		if k != "default" {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	r.sortedCategories = keys
}

// ResolveIcon возвращает строку иконки (например "simple-icons:nginx") по имени сервиса.
func (r *IconResolver) ResolveIcon(name, explicitIcon string) string {
	if explicitIcon != "" {
		return explicitIcon
	}
	n := r.normalizeName(name)
	if v, ok := r.cache.Load(n); ok {
		return v.(*iconEntry).Icon
	}
	if e := r.findEntry(n); e != nil {
		r.cache.Store(n, e)
		return e.Icon
	}
	return r.selectFallbackIcon(n)
}

// ResolveColor возвращает цвет фона для иконки.
func (r *IconResolver) ResolveColor(name string) string {
	n := r.normalizeName(name)
	if v, ok := r.cache.Load(n); ok {
		e := v.(*iconEntry)
		if e.BgColor != "" {
			return e.BgColor
		}
		if c, ok := r.categories[e.Category]; ok && c.BgColor != "" {
			return c.BgColor
		}
	}
	if e := r.findEntry(n); e != nil {
		r.cache.Store(n, e)
		if e.BgColor != "" {
			return e.BgColor
		}
		if c, ok := r.categories[e.Category]; ok && c.BgColor != "" {
			return c.BgColor
		}
	}
	return r.selectFallbackColor(n)
}

// ResolveIconColor возвращает цвет самой иконки.
func (r *IconResolver) ResolveIconColor(name string) string {
	n := r.normalizeName(name)
	if v, ok := r.cache.Load(n); ok {
		e := v.(*iconEntry)
		if e.IconColor != "" {
			return e.IconColor
		}
		if c, ok := r.categories[e.Category]; ok && c.IconColor != "" {
			return c.IconColor
		}
	}
	if e := r.findEntry(n); e != nil {
		r.cache.Store(n, e)
		if e.IconColor != "" {
			return e.IconColor
		}
		if c, ok := r.categories[e.Category]; ok && c.IconColor != "" {
			return c.IconColor
		}
	}
	return r.selectFallbackIconColor(n)
}

// ResolveIconCDN формирует полный URL или data:URI для отображения иконки.
func (r *IconResolver) ResolveIconCDN(name, explicitIcon string) string {
	n := r.normalizeName(name)
	cacheKey := n + "|" + explicitIcon
	if v, ok := r.cdnCache.Load(cacheKey); ok {
		return v.(string)
	}

	icon := strings.TrimSpace(r.ResolveIcon(name, explicitIcon))
	if strings.HasPrefix(icon, "http://") || strings.HasPrefix(icon, "https://") || strings.HasPrefix(icon, "data:") {
		r.cdnCache.Store(cacheKey, icon)
		return icon
	}

	sep := strings.IndexByte(icon, ':')
	if sep == -1 {
		res := r.getCachedFallbackSVG(name)
		r.cdnCache.Store(cacheKey, res)
		return res
	}

	collection := strings.ToLower(strings.TrimSpace(icon[:sep]))
	iconName := strings.ToLower(strings.TrimSpace(icon[sep+1:]))

	color := strings.TrimSpace(r.ResolveIconColor(name))
	color = strings.TrimPrefix(color, "#") // <-- убираем # без пробела
	if color == "" {
		color = "1f2328"
	}

	u := strings.TrimSpace(r.cdnTemplate)
	u = strings.ReplaceAll(u, "{collection}", url.PathEscape(collection))
	u = strings.ReplaceAll(u, "{icon}", url.PathEscape(iconName))
	u = strings.ReplaceAll(u, "{color}", color) // <-- теперь чистый HEX

	r.cdnCache.Store(cacheKey, u)
	return u
}

// ClearCache очищает все внутренние кэши.
func (r *IconResolver) ClearCache() {
	r.cache = sync.Map{}
	r.categoryCache = sync.Map{}
	r.cdnCache = sync.Map{}
	r.svgCache = sync.Map{}
}

// findEntry выполняет поиск наиболее подходящего entry для нормализованного имени.
func (r *IconResolver) findEntry(normalized string) *iconEntry {
	if normalized == "" {
		return nil
	}

	// 1. Точное совпадение по ключу
	if e, ok := r.iconMap[normalized]; ok {
		return e
	}

	// 2. Псевдоним
	if alias, ok := r.aliases[normalized]; ok {
		if e, ok := r.iconMap[alias]; ok {
			return e
		}
	}

	// 3. Поиск по search-терминам (точное совпадение)
	for _, e := range r.entries {
		for _, st := range e.SearchTerms {
			if r.normalizeName(st) == normalized {
				return e
			}
		}
	}

	// 4. Ранжированный поиск с IDF и приоритетом
	queryTokens := strings.Fields(normalized)
	if len(queryTokens) == 0 {
		return nil
	}

	type scored struct {
		entry *iconEntry
		score float64
	}
	var candidates []scored

	for _, e := range r.entries {
		score := 0.0

		// Префикс ключа
		if strings.HasPrefix(e.Key, normalized) {
			score += 10.0
		}

		entryTokens := e.Tokens
		if len(entryTokens) == 0 {
			continue
		}

		intersection := 0.0
		for _, qt := range queryTokens {
			for _, et := range entryTokens {
				if qt == et {
					idf := r.tokenDF[qt]
					intersection += idf
					break
				}
			}
		}

		if intersection > 0 {
			extra := float64(len(entryTokens) - len(queryTokens))
			if extra < 0 {
				extra = 0
			}
			score += intersection / (1.0 + extra*0.5)
		}

		score += float64(e.Priority)

		if score > 0 {
			candidates = append(candidates, scored{e, score})
		}
	}

	if len(candidates) == 0 {
		return nil
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})
	return candidates[0].entry
}

// detectCategory определяет категорию по нормализованному имени.
func (r *IconResolver) detectCategory(normalized string) *Category {
	if normalized == "" {
		return r.categories["default"]
	}
	// Сначала точное совпадение имени категории
	for _, catKey := range r.sortedCategories {
		if strings.Contains(normalized, catKey) || strings.Contains(catKey, normalized) {
			return r.categories[catKey]
		}
	}
	// Затем поиск по ключевым словам
	for _, catKey := range r.sortedCategories {
		cat := r.categories[catKey]
		if cat == nil {
			continue
		}
		for _, kw := range cat.Keywords {
			if kw != "" && strings.Contains(normalized, kw) {
				return cat
			}
		}
	}
	return r.categories["default"]
}

// cachedFindCategory возвращает категорию с кэшированием.
func (r *IconResolver) cachedFindCategory(normalized string) *Category {
	if v, ok := r.categoryCache.Load(normalized); ok {
		return v.(*Category)
	}
	c := r.detectCategory(normalized)
	if c != nil {
		r.categoryCache.Store(normalized, c)
	}
	return c
}

// selectFallbackIcon выбирает fallback-иконку на основе категории.
func (r *IconResolver) selectFallbackIcon(normalized string) string {
	if c := r.cachedFindCategory(normalized); c != nil && len(c.FallbackIcons) > 0 {
		return c.FallbackIcons[r.fnvHash(normalized)%uint32(len(c.FallbackIcons))]
	}
	return r.fallbackIcon
}

// selectFallbackColor возвращает цвет фона для fallback.
func (r *IconResolver) selectFallbackColor(normalized string) string {
	if c := r.cachedFindCategory(normalized); c != nil && c.BgColor != "" {
		return c.BgColor
	}
	return r.generatePastelColor(normalized)
}

// selectFallbackIconColor возвращает цвет иконки для fallback.
func (r *IconResolver) selectFallbackIconColor(normalized string) string {
	if c := r.cachedFindCategory(normalized); c != nil && c.IconColor != "" {
		return c.IconColor
	}
	return ""
}

// generatePastelColor генерирует пастельный цвет на основе строки.
func (r *IconResolver) generatePastelColor(s string) string {
	if s == "" {
		return r.defaultBg
	}
	h := int(r.fnvHash(s) % 360)
	c := r.pastelVal * r.pastelSat
	x := c * (1 - math.Abs(float64((h%60)-30))/30.0)
	m := r.pastelVal - c
	var r1, g1, b1 float64
	switch {
	case h < 60:
		r1, g1, b1 = c, x, 0
	case h < 120:
		r1, g1, b1 = x, c, 0
	case h < 180:
		r1, g1, b1 = 0, c, x
	case h < 240:
		r1, g1, b1 = 0, x, c
	case h < 300:
		r1, g1, b1 = x, 0, c
	default:
		r1, g1, b1 = c, 0, x
	}
	return fmt.Sprintf("#%02X%02X%02X", uint8((r1+m)*255), uint8((g1+m)*255), uint8((b1+m)*255))
}

// getCachedFallbackSVG возвращает data:URI заглушки в виде буквы.
func (r *IconResolver) getCachedFallbackSVG(name string) string {
	char := "?"
	if t := strings.TrimSpace(name); len(t) > 0 {
		char = strings.ToUpper(t[:1])
	}
	if v, ok := r.svgCache.Load(char); ok {
		return v.(string)
	}
	svg := r.generateFallbackSVGForChar(char)
	r.svgCache.Store(char, svg)
	return svg
}

func (r *IconResolver) generateFallbackSVGForChar(char string) string {
	// гарантированно убираем возможные символы #
	bg := strings.TrimPrefix(r.svgBgColor, "#")
	fg := strings.TrimPrefix(r.svgTextColor, "#")
	svg := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="%s" width="40" height="40">
        <rect width="40" height="40" rx="8" fill="#%s"/>
        <text x="20" y="28" text-anchor="middle" font-size="%d" font-family="%s" fill="#%s">%s</text>
    </svg>`, r.svgViewBox, bg, r.svgFontSize, r.svgFontFamily, fg, char)
	return "data:image/svg+xml;charset=utf-8;base64," + base64.StdEncoding.EncodeToString([]byte(svg))
}

func (r *IconResolver) fnvHash(s string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(s))
	return h.Sum32()
}

// normalizeName приводит имя к нижнему регистру, заменяет разделители и удаляет лишнее.
func (r *IconResolver) normalizeName(s string) string {
	s = strings.ToLower(s)
	s = r.normReplacer.Replace(s)
	s = r.normRegex.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}
