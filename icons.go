package main

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"html"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
)

// ─── Типы и константы ─────────────────────────────────────────────────────────

// Category объединяет информацию о категории сервисов.
type Category struct {
	Name          string   // идентификатор категории
	FallbackIcons []string // список иконок для fallback (приоритет по порядку)
	BgColor       string   // цвет фона для категории
	IconColor     string   // цвет иконки для категории
}

// iconEntry описывает иконку и её стили.
type iconEntry struct {
	Icon      string
	BgColor   string
	IconColor string
	Category  string // ссылка на категорию (название)
	KeyLen    int    // длина оригинального ключа (для оптимизации ранжирования)
}

// IconResolver отвечает за подбор иконок по имени сервиса.
type IconResolver struct {
	iconMap       map[string]iconEntry
	iconAliases   map[string]string
	tokenIndex    map[string][]*iconEntry
	cache         sync.Map             // нормализованное имя -> *iconEntry
	categoryCache sync.Map             // нормализованное имя -> *Category
	categories    map[string]*Category // name -> Category
	mu            sync.RWMutex         // для безопасной замены маппингов
}

// IconResolverInterface определяет публичный API.
type IconResolverInterface interface {
	ResolveIcon(name, explicitIcon string) string
	ResolveColor(name string) string
	ResolveIconColor(name string) string
	ResolveIconCDN(name, explicitIcon string) string
}

// ─── Конструктор и инициализация ─────────────────────────────────────────────

// NewIconResolver создаёт новый резолвер, загружая правила из JSON-файла.
// Путь к файлу задаётся переменной окружения ICONS_CONFIG_PATH.
// Если переменная не задана, используется "data/icons.json".
func NewIconResolver() *IconResolver {
	r := &IconResolver{
		iconMap:     make(map[string]iconEntry),
		iconAliases: make(map[string]string),
		tokenIndex:  make(map[string][]*iconEntry),
		categories:  make(map[string]*Category),
	}
	r.initDefaultCategories()

	// Определяем путь к файлу конфигурации иконок
	iconsPath := os.Getenv("ICONS_CONFIG_PATH")
	if iconsPath == "" {
		iconsPath = "data/icons.json"
	}

	if err := r.loadIconsFromJSON(iconsPath); err != nil {
		// Если файла нет или он повреждён, работаем только с категориями (fallback)
		// В продакшене можно залогировать предупреждение, но пока игнорируем
		_ = err
	}
	r.buildIndices()
	return r
}

// loadIconsFromJSON загружает иконки и алиасы из JSON-файла.
// Формат:
//
//	{
//	  "icons": [
//	    {"key": "proxmox", "icon": "simple-icons:proxmox", "bg": "#FDE8D0", "color": "#E57000", "category": "virtualization"}
//	  ],
//	  "aliases": {"postgres": "postgresql"}
//	}
func (r *IconResolver) loadIconsFromJSON(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var cfg struct {
		Icons []struct {
			Key      string `json:"key"`
			Icon     string `json:"icon"`
			Bg       string `json:"bg"`
			Color    string `json:"color"`
			Category string `json:"category"`
		} `json:"icons"`
		Aliases map[string]string `json:"aliases"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return err
	}
	// Очищаем существующие маппинги (если повторно загружаем)
	r.iconMap = make(map[string]iconEntry)
	r.iconAliases = make(map[string]string)

	for _, ic := range cfg.Icons {
		entry := iconEntry{
			Icon:      ic.Icon,
			BgColor:   ic.Bg,
			IconColor: ic.Color,
			Category:  ic.Category,
			KeyLen:    len(ic.Key),
		}
		r.iconMap[ic.Key] = entry
	}
	for alias, target := range cfg.Aliases {
		r.iconAliases[alias] = target
	}
	return nil
}

// initDefaultCategories определяет стандартные категории (используются для fallback).
func (r *IconResolver) initDefaultCategories() {
	categories := []Category{
		{Name: "db", FallbackIcons: []string{"simple-icons:postgresql", "simple-icons:mysql", "mdi:database"}, BgColor: "#E3F2FD", IconColor: "#336791"},
		{Name: "web", FallbackIcons: []string{"mdi:web", "mdi:server", "simple-icons:nginx"}, BgColor: "#DFF5E6", IconColor: "#009639"},
		{Name: "network", FallbackIcons: []string{"mdi:router-wireless", "mdi:lan", "mdi:cloud"}, BgColor: "#E3F2FD", IconColor: "#607080"},
		{Name: "dns", FallbackIcons: []string{"mdi:dns", "mdi:server-network", "simple-icons:cloudflare"}, BgColor: "#FDE8D0", IconColor: "#F48120"},
		{Name: "security", FallbackIcons: []string{"mdi:shield", "mdi:lock", "simple-icons:pi-hole"}, BgColor: "#FCE4E4", IconColor: "#96060C"},
		{Name: "media", FallbackIcons: []string{"mdi:video", "mdi:music", "mdi:television"}, BgColor: "#FDF3D0", IconColor: "#EBAF00"},
		{Name: "storage", FallbackIcons: []string{"mdi:harddisk", "mdi:nas", "mdi:cloud-outline"}, BgColor: "#D9EDF2", IconColor: "#0082C9"},
		{Name: "container", FallbackIcons: []string{"mdi:docker", "mdi:cube-outline", "mdi:package-variant"}, BgColor: "#E3F2FD", IconColor: "#2496ED"},
		{Name: "orchestration", FallbackIcons: []string{"mdi:kubernetes", "mdi:orbit", "mdi:layers"}, BgColor: "#D9EDF2", IconColor: "#326CE5"},
		{Name: "monitoring", FallbackIcons: []string{"mdi:chart-line", "mdi:eye", "mdi:gauge"}, BgColor: "#FDE8D0", IconColor: "#F46800"},
		{Name: "smarthome", FallbackIcons: []string{"mdi:home-automation", "mdi:lightbulb", "mdi:home"}, BgColor: "#E3F2FD", IconColor: "#41BDF5"},
		{Name: "chat", FallbackIcons: []string{"mdi:message", "mdi:chat", "mdi:account-group"}, BgColor: "#E3F2FD", IconColor: "#26A5E4"},
		{Name: "dev", FallbackIcons: []string{"mdi:code-braces", "mdi:git", "mdi:terminal"}, BgColor: "#E6F5E0", IconColor: "#609926"},
		{Name: "editor", FallbackIcons: []string{"mdi:file-code", "mdi:pencil", "mdi:note-text"}, BgColor: "#F0F0F0", IconColor: "#7C3AED"},
		{Name: "virtualization", FallbackIcons: []string{"mdi:server", "mdi:monitor-multiple", "mdi:cpu-64-bit"}, BgColor: "#E3F2FD", IconColor: "#E57000"},
		{Name: "iot", FallbackIcons: []string{"mdi:access-point", "mdi:wifi", "mdi:bluetooth"}, BgColor: "#E6F5E0", IconColor: ""},
		{Name: "os", FallbackIcons: []string{"mdi:desktop-classic", "mdi:linux", "mdi:microsoft-windows"}, BgColor: "#F0F0F0", IconColor: ""},
		{Name: "browser", FallbackIcons: []string{"mdi:web", "mdi:earth", "mdi:application"}, BgColor: "#FDE8D0", IconColor: ""},
		{Name: "gaming", FallbackIcons: []string{"mdi:gamepad", "mdi:joystick", "mdi:television"}, BgColor: "#F0F0F0", IconColor: ""},
		{Name: "default", FallbackIcons: []string{"mdi:server", "mdi:application", "mdi:help-circle"}, BgColor: "", IconColor: ""},
	}
	for _, cat := range categories {
		c := cat
		r.categories[cat.Name] = &c
	}
}

// buildIndices строит инвертированный индекс токенов.
func (r *IconResolver) buildIndices() {
	r.tokenIndex = make(map[string][]*iconEntry)
	for key, entry := range r.iconMap {
		entryCopy := entry
		for _, token := range tokenize(key) {
			r.tokenIndex[token] = append(r.tokenIndex[token], &entryCopy)
		}
	}
}

// ─── Публичные методы резолвера ───────────────────────────────────────────────

// ResolveIcon возвращает идентификатор иконки.
func (r *IconResolver) ResolveIcon(name, explicitIcon string) string {
	if explicitIcon != "" {
		return explicitIcon
	}
	normalized := normalizeName(name)
	if entry := r.cachedFindEntry(normalized); entry != nil {
		return entry.Icon
	}
	return r.selectFallbackIcon(normalized)
}

// ResolveColor возвращает цвет фона для иконки.
func (r *IconResolver) ResolveColor(name string) string {
	normalized := normalizeName(name)
	if entry := r.cachedFindEntry(normalized); entry != nil {
		if entry.BgColor != "" {
			return entry.BgColor
		}
		if cat, ok := r.categories[entry.Category]; ok && cat.BgColor != "" {
			return cat.BgColor
		}
	}
	return r.selectFallbackColor(normalized)
}

// ResolveIconColor возвращает цвет иконки.
func (r *IconResolver) ResolveIconColor(name string) string {
	normalized := normalizeName(name)
	if entry := r.cachedFindEntry(normalized); entry != nil {
		if entry.IconColor != "" {
			return entry.IconColor
		}
		if cat, ok := r.categories[entry.Category]; ok && cat.IconColor != "" {
			return cat.IconColor
		}
	}
	return r.selectFallbackIconColor(normalized)
}

// ResolveIconCDN формирует URL для иконки через Iconify CDN.
func (r *IconResolver) ResolveIconCDN(name, explicitIcon string) string {
	icon := r.ResolveIcon(name, explicitIcon)
	if strings.HasPrefix(icon, "http://") || strings.HasPrefix(icon, "https://") {
		return icon
	}
	// Безопасная проверка формата "prefix:name"
	parts := strings.SplitN(icon, ":", 2)
	if len(parts) != 2 {
		return r.generateFallbackSVG(name)
	}
	base := fmt.Sprintf("https://api.iconify.design/%s/%s.svg",
		url.PathEscape(parts[0]), url.PathEscape(parts[1]))
	brandColor := r.ResolveIconColor(name)
	if brandColor == "" {
		return base
	}
	return base + "?color=" + url.QueryEscape(brandColor)
}

// ClearCache очищает внутренний кэш сопоставлений нормализованных имён и иконок.
func (r *IconResolver) ClearCache() {
	r.cache.Range(func(key, value interface{}) bool {
		r.cache.Delete(key)
		return true
	})
	r.categoryCache.Range(func(key, value interface{}) bool {
		r.categoryCache.Delete(key)
		return true
	})
}

// ─── Внутренние методы поиска ─────────────────────────────────────────────────

// cachedFindEntry находит entry, используя кэш.
func (r *IconResolver) cachedFindEntry(normalized string) *iconEntry {
	if val, ok := r.cache.Load(normalized); ok {
		return val.(*iconEntry)
	}
	entry := r.findEntry(normalized)
	if entry != nil {
		r.cache.Store(normalized, entry)
	}
	return entry
}

// findEntry ищет подходящий iconEntry без использования глобального кэша.
func (r *IconResolver) findEntry(normalized string) *iconEntry {
	// 1. Алиас
	if alias, ok := r.iconAliases[normalized]; ok {
		if e, exists := r.iconMap[alias]; exists {
			return &e
		}
	}
	// 2. Точное совпадение
	if e, exists := r.iconMap[normalized]; exists {
		return &e
	}
	// 3. Поиск по токенам через инвертированный индекс
	tokens := tokenize(normalized)
	if len(tokens) > 0 {
		candidates := make(map[*iconEntry]int)
		for _, token := range tokens {
			for _, e := range r.tokenIndex[token] {
				candidates[e]++
			}
		}
		var best *iconEntry
		bestScore := 0
		for e, cnt := range candidates {
			score := cnt*100 + e.KeyLen
			if score > bestScore {
				bestScore = score
				best = e
			}
		}
		if best != nil {
			return best
		}
	}
	return nil
}

// detectCategory определяет категорию на основе токенов нормализованного имени.
func (r *IconResolver) detectCategory(normalized string) *Category {
	tokens := tokenize(normalized)
	for _, token := range tokens {
		for catName, cat := range r.categories {
			if catName == "default" {
				continue
			}
			if strings.Contains(token, catName) || strings.Contains(catName, token) {
				return cat
			}
		}
	}
	return r.categories["default"]
}

// cachedFindCategory возвращает категорию с кэшированием.
func (r *IconResolver) cachedFindCategory(normalized string) *Category {
	if val, ok := r.categoryCache.Load(normalized); ok {
		return val.(*Category)
	}
	cat := r.detectCategory(normalized)
	if cat != nil {
		r.categoryCache.Store(normalized, cat)
	}
	return cat
}

// selectFallbackIcon подбирает иконку из категории.
func (r *IconResolver) selectFallbackIcon(normalized string) string {
	cat := r.cachedFindCategory(normalized)
	if cat != nil && len(cat.FallbackIcons) > 0 {
		idx := r.fnvHash(normalized) % uint32(len(cat.FallbackIcons))
		return cat.FallbackIcons[idx]
	}
	return "mdi:server"
}

// selectFallbackColor подбирает цвет фона на основе категории.
func (r *IconResolver) selectFallbackColor(normalized string) string {
	cat := r.cachedFindCategory(normalized)
	if cat != nil && cat.BgColor != "" {
		return cat.BgColor
	}
	return r.generatePastelColor(normalized)
}

// selectFallbackIconColor подбирает цвет иконки на основе категории.
func (r *IconResolver) selectFallbackIconColor(normalized string) string {
	cat := r.cachedFindCategory(normalized)
	if cat != nil && cat.IconColor != "" {
		return cat.IconColor
	}
	return ""
}

// generatePastelColor генерирует детерминированный пастельный цвет на основе HSV.
func (r *IconResolver) generatePastelColor(s string) string {
	if s == "" {
		return "#E3F2FD"
	}
	h := r.fnvHash(s) % 360 // Hue 0..359
	sat := 0.3              // Насыщенность 30%
	val := 0.9              // Яркость 90%
	// Преобразование HSV в RGB
	c := val * sat
	x := c * (1 - float64((h%60))/60)
	m := val - c
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
	r2 := uint8((r1 + m) * 255)
	g2 := uint8((g1 + m) * 255)
	b2 := uint8((b1 + m) * 255)
	return fmt.Sprintf("#%02X%02X%02X", r2, g2, b2)
}

// generateFallbackSVG создаёт SVG-картинку с первой буквой имени (data URI).
func (r *IconResolver) generateFallbackSVG(name string) string {
	letter := "?"
	trimmed := strings.TrimSpace(name)
	if len(trimmed) > 0 {
		first := strings.ToUpper(trimmed[:1])
		letter = html.EscapeString(first)
	}
	escaped := url.QueryEscape(fmt.Sprintf(
		`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 40 40">`+
			`<rect width="40" height="40" rx="8" fill="#E3F2FD"/>`+
			`<text x="20" y="28" text-anchor="middle" font-size="22" font-family="Arial,sans-serif" fill="#1f2328">%s</text></svg>`, letter))
	return "data:image/svg+xml;charset=utf-8," + escaped
}

// fnvHash – хеш для детерминированного выбора.
func (r *IconResolver) fnvHash(s string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(s))
	return h.Sum32()
}

// ─── Вспомогательные функции (без состояния) ─────────────────────────────────

var nameNormalizer = regexp.MustCompile(`[^a-zа-яё0-9 ]+`)

// normalizeName приводит строку к нижнему регистру, заменяет разделители на пробелы, удаляет лишние символы.
func normalizeName(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "-", " ")
	s = strings.ReplaceAll(s, "_", " ")
	s = strings.ReplaceAll(s, ".", " ")
	s = nameNormalizer.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}

// tokenize разбивает нормализованное имя на слова.
func tokenize(s string) []string {
	return strings.Fields(normalizeName(s))
}
