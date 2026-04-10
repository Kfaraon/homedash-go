package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Service struct {
	Name      string `json:"name"`
	URL       string `json:"url"`
	IP        string `json:"ip"`
	Icon      string `json:"icon,omitempty"`
	VerifySSL bool   `json:"verify_ssl"`
}

type Group struct {
	Name     string    `json:"name"`
	Services []Service `json:"services"`
}

type Status struct {
	Available bool  `json:"available"`
	HTTP      *bool `json:"http"`
	Ping      *bool `json:"ping"`
}

var (
	configFile = "config.json"
	cache      = struct {
		mu   sync.RWMutex
		data map[string]Status
		ts   time.Time
	}{data: make(map[string]Status)}
	cacheTTL     = 3 * time.Second
	serverPort   = getEnv("PORT", "5000")
	checkTimeout = getDurationEnv("CHECK_TIMEOUT", 2*time.Second)
	pingTimeout  = getDurationEnv("PING_TIMEOUT", 1*time.Second)
)

// getEnv получает переменную окружения с fallback
func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

// getDurationEnv получает переменную окружения как duration
func getDurationEnv(key string, fallback time.Duration) time.Duration {
	if value := os.Getenv(key); value != "" {
		if d, err := time.ParseDuration(value); err == nil {
			return d
		}
	}
	return fallback
}

func main() {
	// Загружаем конфигурацию
	groups, err := loadGroups()
	if err != nil {
		log.Fatal("Ошибка загрузки конфига:", err)
	}

	// Валидируем конфигурацию
	if err := validateGroups(groups); err != nil {
		log.Printf("Предупреждение валидации: %v", err)
	}

	// Добавляем функции в шаблоны
	funcMap := template.FuncMap{
		"lower":            strings.ToLower,
		"resolveIcon":      resolveIcon,
		"resolveColor":     resolveColor,
		"resolveIconColor": resolveIconColor,
		"resolveIconCDN":   resolveIconCDN,
	}
	tmpl := template.Must(template.New("home.html").Funcs(funcMap).ParseFiles("templates/home.html"))

	mux := http.NewServeMux()

	// Статические файлы
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))

	// Главная страница
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		err := tmpl.ExecuteTemplate(w, "home.html", map[string]interface{}{
			"groups": groups,
		})
		if err != nil {
			log.Println("Ошибка рендеринга шаблона:", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})

	// API статусов
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		statuses := getCachedStatuses(groups)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(statuses)
	})

	// Health check endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{
			"status":    "ok",
			"timestamp": time.Now().Format(time.RFC3339),
		})
	})

	addr := ":" + serverPort
	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  30 * time.Second,
	}

	// Graceful shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Printf("Сервер запущен на http://localhost:%s", serverPort)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Ошибка запуска сервера: %v", err)
		}
	}()

	<-stop
	log.Println("Получен сигнал остановки, завершение работы...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("Ошибка завершения работы: %v", err)
	}
	log.Println("Сервер успешно остановлен")
}

func loadGroups() ([]Group, error) {
	data, err := os.ReadFile(configFile)
	if err != nil {
		return nil, fmt.Errorf("чтение файла %s: %w", configFile, err)
	}

	var root struct {
		Groups []Group `json:"groups"`
	}

	// Поддержка двух форматов: объект с groups
	if err := json.Unmarshal(data, &root); err == nil && len(root.Groups) > 0 {
		return root.Groups, nil
	}

	// Если это список сервисов без групп
	var services []Service
	if err := json.Unmarshal(data, &services); err == nil {
		if len(services) > 0 {
			return []Group{{Name: "Все сервисы", Services: services}}, nil
		}
	}

	return nil, fmt.Errorf("неверный формат конфигурации: ожидается объект 'groups' или список сервисов")
}

// validateGroups проверяет конфигурацию на корректность
func validateGroups(groups []Group) error {
	if len(groups) == 0 {
		return fmt.Errorf("список групп пуст")
	}

	seen := make(map[string]bool)
	for i, g := range groups {
		if g.Name == "" {
			return fmt.Errorf("группа #%d: пустое имя", i)
		}

		for j, s := range g.Services {
			if s.Name == "" {
				return fmt.Errorf("группа '%s', сервис #%d: пустое имя", g.Name, j)
			}
			if s.URL == "" && s.IP == "" {
				return fmt.Errorf("группа '%s', сервис '%s': не указан URL или IP", g.Name, s.Name)
			}

			// Проверка дубликатов имён
			key := strings.ToLower(s.Name)
			if seen[key] {
				log.Printf("Предупреждение: дубликат имени сервиса '%s'", s.Name)
			}
			seen[key] = true
		}
	}
	return nil
}

func getCachedStatuses(groups []Group) map[string]interface{} {
	cache.mu.RLock()
	if time.Since(cache.ts) < cacheTTL && cache.data != nil {
		defer cache.mu.RUnlock()
		return map[string]interface{}{
			"services":  cache.data,
			"timestamp": time.Now().Format(time.RFC3339),
		}
	}
	cache.mu.RUnlock()

	cache.mu.Lock()
	defer cache.mu.Unlock()
	// Обновляем кеш
	statusMap := make(map[string]Status)
	var wg sync.WaitGroup
	for _, g := range groups {
		for _, s := range g.Services {
			wg.Add(1)
			go func(s Service) {
				defer wg.Done()
				status := checkService(s)
				log.Printf("Статус %s: доступен=%v, http=%v, ping=%v", s.Name, status.Available, status.HTTP, status.Ping)
				statusMap[s.Name] = status
			}(s)
		}
	}
	wg.Wait()
	cache.data = statusMap
	cache.ts = time.Now()
	return map[string]interface{}{
		"services":  cache.data,
		"timestamp": cache.ts.Format(time.RFC3339),
	}
}

func checkService(s Service) Status {
	var httpOk *bool
	var pingOk *bool

	// HTTP проверка
	if s.URL != "" {
		ok := checkHTTP(s.URL, s.VerifySSL)
		httpOk = &ok
	}
	// Ping проверка
	if s.IP != "" {
		ok := checkPing(s.IP)
		pingOk = &ok
	}

	available := false
	if s.URL != "" && s.IP != "" {
		available = (httpOk != nil && *httpOk) || (pingOk != nil && *pingOk)
	} else if s.URL != "" {
		available = httpOk != nil && *httpOk
	} else if s.IP != "" {
		available = pingOk != nil && *pingOk
	}

	return Status{
		Available: available,
		HTTP:      httpOk,
		Ping:      pingOk,
	}
}

func checkHTTP(url string, verifySSL bool) bool {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: !verifySSL},
	}
	client := &http.Client{
		Transport: tr,
		Timeout:   checkTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		log.Printf("HTTP запрос ошибка: %v", err)
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("HTTP ошибка для %s: %v", url, err)
		return false
	}
	defer resp.Body.Close()
	return true
}

// checkPing использует системную команду ping (работает без привилегий)
func checkPing(ip string) bool {
	// Удаляем порт, если есть
	if idx := strings.Index(ip, ":"); idx != -1 {
		ip = ip[:idx]
	}
	var cmd *exec.Cmd
	timeoutSec := int(pingTimeout.Seconds())
	if timeoutSec < 1 {
		timeoutSec = 1
	}
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("ping", "-n", "1", "-w", fmt.Sprintf("%d", timeoutSec*1000), ip)
	default: // Linux, macOS
		cmd = exec.Command("ping", "-c", "1", "-W", fmt.Sprintf("%d", timeoutSec), ip)
	}
	err := cmd.Run()
	if err != nil {
		log.Printf("Ping ошибка для %s: %v", ip, err)
		return false
	}
	return true
}

// iconMap — маппинг названий сервисов на иконки и цвета фона bubble
var iconMap = map[string]struct {
	Icon      string
	BgColor   string // фон bubble
	IconColor string // оригинальный цвет бренда (для single-color иконок)
}{
	// Виртуализация и инфраструктура
	"proxmox":    {"simple-icons:proxmox", "#FDE8D0", "#E57000"},
	"vmware":     {"simple-icons:vmware", "#E3F2FD", "#607080"},
	"virtualbox": {"simple-icons:virtualbox", "#E3F2FD", "#184A84"},
	"hyper-v":    {"mdi:microsoft-hyper-v", "#E3F2FD", ""},

	// Сеть и DNS
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

	// Умный дом
	"home assistant": {"simple-icons:homeassistant", "#E3F2FD", "#41BDF5"},
	"hass":           {"simple-icons:homeassistant", "#E3F2FD", "#41BDF5"},
	"homebridge":     {"simple-icons:homebridge", "#FCE4E4", "#491F5E"},
	"domoticz":       {"mdi:home-automation", "#E6F5E0", ""},
	"ioBroker":       {"simple-icons:iobroker", "#D9EDF2", ""},

	// Контейнеры и оркестрация
	"docker":     {"simple-icons:docker", "#E3F2FD", "#2496ED"},
	"podman":     {"simple-icons:podman", "#E3F2FD", "#892CA0"},
	"kubernetes": {"simple-icons:kubernetes", "#D9EDF2", "#326CE5"},
	"k8s":        {"simple-icons:kubernetes", "#D9EDF2", "#326CE5"},
	"portainer":  {"simple-icons:portainer", "#E6F5E0", "#65BC40"},
	"rancher":    {"simple-icons:rancher", "#D9EDF2", "#009DDD"},

	// Мониторинг и логи
	"grafana":     {"simple-icons:grafana", "#FDE8D0", "#F46800"},
	"prometheus":  {"simple-icons:prometheus", "#FCE4E4", "#E6522C"},
	"datadog":     {"simple-icons:datadog", "#FDE8D0", "#632CA6"},
	"uptime-kuma": {"simple-icons:uptime-kuma", "#E6F5E0", "#5CCE3B"},
	"zabbix":      {"simple-icons:zabbix", "#E3F2FD", "#C41E3A"},
	"netdata":     {"simple-icons:netdata", "#FCE4E4", "#00AB44"},

	// Веб-серверы
	"nginx":   {"simple-icons:nginx", "#DFF5E6", "#009639"},
	"apache":  {"simple-icons:apache", "#FCE4E4", "#D22128"},
	"caddy":   {"simple-icons:caddy", "#FDE8D0", "#22313F"},
	"traefik": {"simple-icons:traefik", "#D9EDF2", "#24A5BE"},
	"haproxy": {"simple-icons:haproxy", "#FCE4E4", "#1064A8"},

	// Базы данных
	"mysql":         {"simple-icons:mysql", "#E3F2FD", "#4479A1"},
	"mariadb":       {"simple-icons:mariadb", "#E3F2FD", "#003545"},
	"postgres":      {"simple-icons:postgresql", "#E3F2FD", "#336791"},
	"postgresql":    {"simple-icons:postgresql", "#E3F2FD", "#336791"},
	"redis":         {"simple-icons:redis", "#FCE4E4", "#DC382D"},
	"mongodb":       {"simple-icons:mongodb", "#E6F5E0", "#47A248"},
	"elasticsearch": {"simple-icons:elasticsearch", "#D9EDF2", "#005571"},
	"influxdb":      {"simple-icons:influxdb", "#D9EDF2", "#22ADF6"},
	"sqlite":        {"simple-icons:sqlite", "#E3F2FD", "#003B57"},

	// Безопасность
	"pihole":      {"simple-icons:pi-hole", "#FCE4E4", "#96060C"},
	"pi-hole":     {"simple-icons:pi-hole", "#FCE4E4", "#96060C"},
	"bitwarden":   {"simple-icons:bitwarden", "#DCE6F8", "#175DDC"},
	"vaultwarden": {"simple-icons:vaultwarden", "#E6F0FA", "#5B9FE5"},
	"wireguard":   {"simple-icons:wireguard", "#FCE4E4", "#88171A"},
	"tailscale":   {"simple-icons:tailscale", "#E8E8EE", "#24243A"},
	"authentik":   {"simple-icons:authentik", "#D9EDF2", "#FD4B2D"},
	"authelia":    {"simple-icons:authelia", "#E3F2FD", "#0047AB"},

	// Медиа и развлечения
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

	// Облачные хранилища и NAS
	"nextcloud": {"simple-icons:nextcloud", "#D9EDF2", "#0082C9"},
	"owncloud":  {"simple-icons:owncloud", "#D9EDF2", "#041E42"},
	"truenas":   {"simple-icons:truenas", "#D9EDF2", "#0095D5"},
	"synology":  {"simple-icons:synology", "#F0F0F0", "#B5B5B5"},
	"qnap":      {"simple-icons:qnap", "#F0F0F0", ""},
	"minio":     {"simple-icons:minio", "#FCE4E4", "#C72E49"},

	// CI/CD и разработка
	"github":    {"simple-icons:github", "#E3E3E3", "#181717"},
	"gitlab":    {"simple-icons:gitlab", "#FDE8D0", "#FC6D26"},
	"gitea":     {"simple-icons:gitea", "#E6F5E0", "#609926"},
	"jenkins":   {"simple-icons:jenkins", "#FCE4E4", "#D24939"},
	"sonarqube": {"simple-icons:sonarqube", "#D9EDF2", "#549DD1"},

	// Коммуникации
	"telegram":   {"simple-icons:telegram", "#E3F2FD", "#26A5E4"},
	"discord":    {"simple-icons:discord", "#E3F2FD", "#5865F2"},
	"slack":      {"simple-icons:slack", "#E8E8EE", "#4A154B"},
	"mattermost": {"simple-icons:mattermost", "#D9EDF2", "#0058CC"},
	"matrix":     {"simple-icons:matrix", "#E6F5E0", "#000000"},
	"zulip":      {"simple-icons:zulip", "#E3F2FD", "#6494D8"},

	// Прочее
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

// fallbackIcons — запасные иконки по категориям
var fallbackIcons = []string{
	"mdi:server",
	"mdi:web",
	"mdi:network",
	"mdi:application",
	"mdi:cloud",
	"mdi:lan",
}

// iconColors — пастельные цвета для неизвестных сервисов
var pastelColors = []string{
	"#E8E5FF", // pastel indigo
	"#FCE4EC", // pastel pink
	"#E0F2F1", // pastel teal
	"#FFF3E0", // pastel amber
	"#EDE7F6", // pastel violet
	"#FFEBEE", // pastel red
	"#E8F5E9", // pastel green
	"#E3F2FD", // pastel blue
	"#FFF3E0", // pastel orange
	"#F1F8E9", // pastel lime
}

// generatePastelColor создаёт пастельный цвет по хешу имени
func generatePastelColor(s string) string {
	h := fnv.New32a()
	h.Write([]byte(s))
	return pastelColors[h.Sum32()%uint32(len(pastelColors))]
}

// resolveIcon возвращает иконку сервиса, подбирая по имени если не задана
func resolveIcon(name, icon string) string {
	// Если иконка задана явно — используем её
	if strings.TrimSpace(icon) != "" {
		return icon
	}

	// Подбор по имени (регистронезависимо)
	lowerName := strings.ToLower(strings.TrimSpace(name))
	if entry, ok := iconMap[lowerName]; ok {
		return entry.Icon
	}

	// Пробуем частичное совпадение
	for key, entry := range iconMap {
		if strings.Contains(lowerName, key) || strings.Contains(key, lowerName) {
			return entry.Icon
		}
	}

	// fallback — детерминированная иконка по имени
	h := fnv.New32a()
	h.Write([]byte(lowerName))
	return fallbackIcons[h.Sum32()%uint32(len(fallbackIcons))]
}

// resolveColor возвращает цвет фона bubble для сервиса
func resolveColor(name string) string {
	lowerName := strings.ToLower(strings.TrimSpace(name))

	// Прямое совпадение
	if entry, ok := iconMap[lowerName]; ok && entry.BgColor != "" {
		return entry.BgColor
	}

	// Частичное совпадение
	for key, entry := range iconMap {
		if entry.BgColor == "" {
			continue
		}
		if strings.Contains(lowerName, key) || strings.Contains(key, lowerName) {
			return entry.BgColor
		}
	}

	// Генерация пастельного цвета по хешу имени
	return generatePastelColor(lowerName)
}

// resolveIconColor возвращает оригинальный цвет бренда для иконки
func resolveIconColor(name string) string {
	lowerName := strings.ToLower(strings.TrimSpace(name))

	// Прямое совпадение
	if entry, ok := iconMap[lowerName]; ok && entry.IconColor != "" {
		return entry.IconColor
	}

	// Частичное совпадение
	for key, entry := range iconMap {
		if entry.IconColor == "" {
			continue
		}
		if strings.Contains(lowerName, key) || strings.Contains(key, lowerName) {
			return entry.IconColor
		}
	}

	// Fallback — цвет текста
	return ""
}

// resolveIconCDN возвращает URL иконки с Iconify CDN для использования в <img> теге
// Формат: https://api.iconify.design/{prefix}/{name}.svg?color={color}
// Если цвет не задан — иконка сохраняет оригинальные цвета (multi-color)
func resolveIconCDN(name, explicitIcon string) string {
	icon := resolveIcon(name, explicitIcon)

	// Если это URL (http/https) — возвращаем как есть
	if strings.HasPrefix(icon, "http://") || strings.HasPrefix(icon, "https://") {
		return icon
	}

	// Парсим "prefix:name"
	parts := strings.SplitN(icon, ":", 2)
	if len(parts) != 2 {
		// Fallback — возвращаем простую SVG
		return generateFallbackSVG(name)
	}
	prefix := parts[0]
	namePart := parts[1]

	// Получаем цвет бренда
	brandColor := resolveIconColor(name)

	// Если цвет задан — используем его (для single-color иконок)
	// Если нет — оставляем без параметра color для сохранения оригинальных цветов
	if brandColor == "" {
		return fmt.Sprintf("https://api.iconify.design/%s/%s.svg",
			url.PathEscape(prefix), url.PathEscape(namePart))
	}

	return fmt.Sprintf("https://api.iconify.design/%s/%s.svg?color=%s",
		url.PathEscape(prefix), url.PathEscape(namePart), url.QueryEscape(brandColor))
}

// generateFallbackSVG создаёт простую SVG-заглушку с первой буквой имени
func generateFallbackSVG(name string) string {
	letter := "?"
	if len(name) > 0 {
		letter = strings.ToUpper(string(name[0]))
	}
	escaped := url.QueryEscape(fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 40 40"><rect width="40" height="40" rx="8" fill="#E3F2FD"/><text x="20" y="28" text-anchor="middle" font-size="22" font-family="Arial,sans-serif" fill="#1f2328">%s</text></svg>`, letter))
	return "data:image/svg+xml;charset=utf-8," + escaped
}
