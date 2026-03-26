package main

import (
	"crypto/tls"
	"encoding/json"
	"html/template"
	"io/ioutil"
	"log"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

type Service struct {
	Name      string `json:"name"`
	URL       string `json:"url"`
	IP        string `json:"ip"`
	Icon      string `json:"icon"`
	Category  string `json:"category"`
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
	cacheTTL = 3 * time.Second
)

func main() {
	// Загружаем конфигурацию
	groups, err := loadGroups()
	if err != nil {
		log.Fatal("Ошибка загрузки конфига:", err)
	}

	// Добавляем функцию lower в шаблоны
	funcMap := template.FuncMap{
		"lower": strings.ToLower,
	}
	tmpl := template.Must(template.New("home.html").Funcs(funcMap).ParseFiles("templates/home.html"))

	// Статические файлы
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))

	// Главная страница
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
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
	http.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		statuses := getCachedStatuses(groups)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(statuses)
	})

	log.Println("Сервер запущен на http://localhost:5000")
	log.Fatal(http.ListenAndServe(":5000", nil))
}

func loadGroups() ([]Group, error) {
	data, err := ioutil.ReadFile(configFile)
	if err != nil {
		return nil, err
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
	return nil, nil
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
		Timeout:   2 * time.Second,
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
	// Считаем любой ответ (включая 4xx, 5xx) как то, что сервер отвечает
	return true
}

// checkPing использует системную команду ping (работает без привилегий)
func checkPing(ip string) bool {
	// Удаляем порт, если есть
	if idx := strings.Index(ip, ":"); idx != -1 {
		ip = ip[:idx]
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("ping", "-n", "1", "-w", "1000", ip)
	default: // Linux, macOS
		cmd = exec.Command("ping", "-c", "1", "-W", "1", ip)
	}
	err := cmd.Run()
	if err != nil {
		log.Printf("Ping ошибка для %s: %v", ip, err)
		return false
	}
	return true
}
