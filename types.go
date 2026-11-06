package main

import (
	"html/template"
	"time"
)

// ─── Types ───

// Service описывает один сервис для мониторинга
type Service struct {
	Name      string `json:"name"`
	URL       string `json:"url"`
	IP        string `json:"ip"`
	Icon      string `json:"icon,omitempty"`
	VerifySSL bool   `json:"verify_ssl"` // default: false (SSL verification disabled unless explicitly enabled)
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

// AdminData — данные для шаблона админки
type AdminData struct {
	Groups     []Group
	GroupsJSON template.JS
}

// IPCache holds cached public IP with metadata
type IPCache struct {
	IP        string    `json:"ip"`
	Type      string    `json:"type"` // "ipv4" or "ipv6"
	Provider  string    `json:"provider"`
	FetchedAt time.Time `json:"fetched_at"`
}
