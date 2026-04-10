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

	"github.com/fsnotify/fsnotify"
)

// ─── Types ───

// Service описывает один сервис для мониторинга
type Service struct {
	Name      string `json:"name"`
	URL       string `json:"url"`
	IP        string `json:"ip"`
	Icon      string `json:"icon,omitempty"`
	VerifySSL bool   `json:"verify_ssl"`
}

// Group — логическая группа сервисов
type Group struct {
	Name     string    `json:"name"`
	Services []Service `json:"services"`
}

// Status — результат проверки одного сервиса
type Status struct {
	Available bool  `json:"available"`
	HTTP      *bool `json:"http"`
	Ping      *bool `json:"ping"`
}

// iconEntry — запись в мапе иконок
type iconEntry struct {
	Icon      string
	BgColor   string
	IconColor string
}

// ─── Config ───

var (
	configFile   = "config.json"
	cacheTTL     = 3 * time.Second
	serverPort   = getEnv("PORT", "5000")
	checkTimeout = getDurationEnv("CHECK_TIMEOUT", 2*time.Second)
	pingTimeout  = getDurationEnv("PING_TIMEOUT", 1*time.Second)

	// Глобальное состояние с mutex
	state = struct {
		groupsMu sync.RWMutex
		groups   []Group
		cacheMu  sync.RWMutex
		cache    map[string]Status
		cacheTS  time.Time
	}{
		cache: make(map[string]Status),
	}
)

// ─── Env helpers ───

// getEnv получает переменную окружения с fallback
func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// getDurationEnv получает переменную окружения как duration
func getDurationEnv(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}

// ─── Main ───

func main() {
	// Загрузка и валидация конфига
	var err error
	state.groupsMu.Lock()
	state.groups, err = loadGroups()
	state.groupsMu.Unlock()
	if err != nil {
		log.Fatal("Ошибка загрузки конфига: ", err)
	}
	if err := validateGroups(state.groups); err != nil {
		log.Printf("Предупреждение валидации: %v", err)
	}

	// Hot-reload конфига
	go watchConfig()

	// Шаблон с функциями
	funcs := template.FuncMap{
		"resolveIcon":      resolveIcon,
		"resolveColor":     resolveColor,
		"resolveIconColor": resolveIconColor,
		"resolveIconCDN":   resolveIconCDN,
	}
	tmpl := template.Must(template.New("home.html").Funcs(funcs).ParseFiles("templates/home.html"))

	// Роутер
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
	mux.HandleFunc("/", serveHome(tmpl))
	mux.HandleFunc("/api/status", serveStatus)
	mux.HandleFunc("/health", serveHealth)

	// Сервер с таймаутами
	srv := &http.Server{
		Addr:         ":" + serverPort,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  30 * time.Second,
	}

	// Graceful shutdown
	done := make(chan os.Signal, 1)
	signal.Notify(done, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Printf("Сервер запущен на http://localhost:%s", serverPort)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Ошибка запуска сервера: %v", err)
		}
	}()

	<-done
	log.Println("Получен сигнал остановки, завершение работы...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("Ошибка завершения работы: %v", err)
	}
	log.Println("Сервер успешно остановлен")
}

// ─── Handlers ───

func serveHome(tmpl *template.Template) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		state.groupsMu.RLock()
		g := state.groups
		state.groupsMu.RUnlock()

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := tmpl.ExecuteTemplate(w, "home.html", map[string]any{"groups": g}); err != nil {
			log.Println("Ошибка рендеринга шаблона:", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

func serveStatus(w http.ResponseWriter, _ *http.Request) {
	state.groupsMu.RLock()
	g := state.groups
	state.groupsMu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(getCachedStatuses(g))
}

func serveHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":    "ok",
		"timestamp": time.Now().Format(time.RFC3339),
	})
}

// ─── Config loading & watch ───

func loadGroups() ([]Group, error) {
	data, err := os.ReadFile(configFile)
	if err != nil {
		return nil, fmt.Errorf("чтение %s: %w", configFile, err)
	}

	// Формат с группами
	var root struct{ Groups []Group }
	if err := json.Unmarshal(data, &root); err == nil && len(root.Groups) > 0 {
		return root.Groups, nil
	}

	// Плоский список сервисов
	var svcs []Service
	if err := json.Unmarshal(data, &svcs); err == nil && len(svcs) > 0 {
		return []Group{{Name: "Все сервисы", Services: svcs}}, nil
	}

	return nil, fmt.Errorf("неверный формат: ожидается 'groups' или список сервисов")
}

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
				return fmt.Errorf("группа %q, сервис #%d: пустое имя", g.Name, j)
			}
			if s.URL == "" && s.IP == "" {
				return fmt.Errorf("группа %q, сервис %q: не указан URL или IP", g.Name, s.Name)
			}
			key := strings.ToLower(s.Name)
			if seen[key] {
				log.Printf("Предупреждение: дубликат имени сервиса %q", s.Name)
			}
			seen[key] = true
		}
	}
	return nil
}

func watchConfig() {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("Ошибка создания watcher: %v", err)
		return
	}
	defer w.Close()

	if err := w.Add("."); err != nil {
		log.Printf("Ошибка добавления %s в watcher: %v", configFile, err)
		return
	}
	log.Printf("Watching %s for changes...", configFile)

	for {
		select {
		case e, ok := <-w.Events:
			if !ok {
				return
			}
			if e.Name == configFile && (e.Op&fsnotify.Write != 0 || e.Op&fsnotify.Create != 0) {
				log.Println("Обнаружено изменение config.json, перезагрузка...")
				reloadConfig()
			}
		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			log.Printf("Ошибка watcher: %v", err)
		}
	}
}

func reloadConfig() {
	g, err := loadGroups()
	if err != nil {
		log.Printf("Ошибка перезагрузки конфига: %v", err)
		return
	}
	if err := validateGroups(g); err != nil {
		log.Printf("Предупреждение валидации: %v", err)
	}

	state.groupsMu.Lock()
	state.groups = g
	state.groupsMu.Unlock()

	// Сброс кэша
	state.cacheMu.Lock()
	state.cache = make(map[string]Status)
	state.cacheTS = time.Time{}
	state.cacheMu.Unlock()

	n := 0
	for _, gr := range g {
		n += len(gr.Services)
	}
	log.Printf("Конфиг перезагружен: %d групп, %d сервисов", len(g), n)
}

// ─── Status checking ───

func getCachedStatuses(groups []Group) map[string]any {
	state.cacheMu.RLock()
	if time.Since(state.cacheTS) < cacheTTL && state.cache != nil {
		defer state.cacheMu.RUnlock()
		return statusResp(state.cache)
	}
	state.cacheMu.RUnlock()

	state.cacheMu.Lock()
	defer state.cacheMu.Unlock()

	// Повторная проверка после acquire lock
	if time.Since(state.cacheTS) < cacheTTL && state.cache != nil {
		return statusResp(state.cache)
	}

	sm := make(map[string]Status)
	var wg sync.WaitGroup
	for _, g := range groups {
		for _, s := range g.Services {
			wg.Add(1)
			go func(svc Service) {
				defer wg.Done()
				st := checkService(svc)
				log.Printf("Статус %s: доступен=%v, http=%v, ping=%v",
					svc.Name, st.Available, ptrBool(st.HTTP), ptrBool(st.Ping))
				sm[svc.Name] = st
			}(s)
		}
	}
	wg.Wait()

	state.cache = sm
	state.cacheTS = time.Now()
	return statusResp(sm)
}

func statusResp(services map[string]Status) map[string]any {
	return map[string]any{
		"services":  services,
		"timestamp": time.Now().Format(time.RFC3339),
	}
}

func ptrBool(p *bool) any {
	if p == nil {
		return "n/a"
	}
	return *p
}

func checkService(s Service) Status {
	var httpOk, pingOk *bool

	if s.URL != "" {
		ok := checkHTTP(s.URL, s.VerifySSL)
		httpOk = &ok
	}
	if s.IP != "" {
		ok := checkPing(s.IP)
		pingOk = &ok
	}

	avail := false
	switch {
	case s.URL != "" && s.IP != "":
		avail = (httpOk != nil && *httpOk) || (pingOk != nil && *pingOk)
	case s.URL != "":
		avail = httpOk != nil && *httpOk
	case s.IP != "":
		avail = pingOk != nil && *pingOk
	}

	return Status{Available: avail, HTTP: httpOk, Ping: pingOk}
}

func checkHTTP(u string, verifySSL bool) bool {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: !verifySSL}, //nolint:gosec
	}
	client := &http.Client{
		Transport: tr,
		Timeout:   checkTimeout,
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		log.Printf("HTTP запрос ошибка: %v", err)
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("HTTP ошибка для %s: %v", u, err)
		return false
	}
	defer resp.Body.Close()
	return true
}

func checkPing(ip string) bool {
	if idx := strings.Index(ip, ":"); idx != -1 {
		ip = ip[:idx]
	}
	timeoutSec := int(pingTimeout.Seconds())
	if timeoutSec < 1 {
		timeoutSec = 1
	}

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("ping", "-n", "1", "-w", fmt.Sprintf("%d", timeoutSec*1000), ip)
	default:
		cmd = exec.Command("ping", "-c", "1", "-W", fmt.Sprintf("%d", timeoutSec), ip)
	}

	if err := cmd.Run(); err != nil {
		log.Printf("Ping ошибка для %s: %v", ip, err)
		return false
	}
	return true
}

// ─── Icon resolution ───

// iconMap — маппинг названий сервисов на иконки и цвета
//
//nolint:gochecknoglobals
var iconMap = map[string]iconEntry{
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
	"iobroker":       {"simple-icons:iobroker", "#D9EDF2", ""},

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

var fallbackIcons = []string{
	"mdi:server", "mdi:web", "mdi:network",
	"mdi:application", "mdi:cloud", "mdi:lan",
}

var pastelColors = []string{
	"#E8E5FF", "#FCE4EC", "#E0F2F1", "#FFF3E0", "#EDE7F6",
	"#FFEBEE", "#E8F5E9", "#E3F2FD", "#FFF3E0", "#F1F8E9",
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
	lower := strings.ToLower(strings.TrimSpace(name))
	if e, ok := iconMap[lower]; ok {
		return e.Icon
	}
	for key, e := range iconMap {
		if strings.Contains(lower, key) || strings.Contains(key, lower) {
			return e.Icon
		}
	}
	h := fnv.New32a()
	h.Write([]byte(lower))
	return fallbackIcons[h.Sum32()%uint32(len(fallbackIcons))]
}

func resolveColor(name string) string {
	lower := strings.ToLower(strings.TrimSpace(name))
	if e, ok := iconMap[lower]; ok && e.BgColor != "" {
		return e.BgColor
	}
	for key, e := range iconMap {
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
	lower := strings.ToLower(strings.TrimSpace(name))
	if e, ok := iconMap[lower]; ok && e.IconColor != "" {
		return e.IconColor
	}
	for key, e := range iconMap {
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
