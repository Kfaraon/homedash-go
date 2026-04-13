package main

import "html/template"

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

// AdminData — данные для шаблона админки
type AdminData struct {
	Groups     []Group
	GroupsJSON template.JS
}
