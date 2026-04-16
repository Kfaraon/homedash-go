# homedash-go — API Reference

## Public Endpoints

### `GET /` — Home page
Returns the main dashboard HTML page with service cards.

**Response:** `text/html`

---

### `GET /api/status` — Service status
Returns current status of all monitored services.

**Response:** `application/json`

```json
{
  "services": {
    "Grafana": {
      "available": true,
      "http": true,
      "ping": true
    }
  },
  "timestamp": "2024-01-15T10:30:00Z"
}
```

**Caching:** 3s TTL + stale-while-revalidate (up to 15s)

---

### `GET /health` — Health check
Returns application health status.

**Response:** `application/json`

```json
{
  "status": "ok",
  "config_ok": true,
  "cache_entries": 10,
  "groups_count": 2,
  "timestamp": "2024-01-15T10:30:00Z"
}
```

**Status values:**
- `ok` — all systems operational
- `degraded` — config file has issues

---

### `GET /api/myip` — Public IP
Returns the server's public IP address.

**Response:** `text/plain`

```
203.0.113.42
```

---

### `GET /api/metrics` — Application metrics (JSON)
Returns internal metrics in JSON format for frontend consumption.

**Response:** `application/json`

```json
{
  "checks_total": {"Grafana": 150, "Nginx": 148},
  "checks_failed": {"Nginx": 2},
  "check_duration_s": {"Grafana": 0.045},
  "config_reloads": 3,
  "cache_hits": 500,
  "cache_misses": 12,
  "active_checks": 8,
  "circuit_breakers": {"Nginx": "open"},
  "ip_fetches": 12,
  "ip_fetch_errors": 0,
  "timestamp": "2024-01-15T10:30:00Z"
}
```

---

### `GET /metrics` — Prometheus metrics
Returns metrics in Prometheus text format.

**Response:** `text/plain; version=0.0.4`

```
# HELP homedash_config_reloads_total Total number of config reloads
# TYPE homedash_config_reloads_total counter
homedash_config_reloads_total 3

# HELP homedash_checks_total Total number of checks per service
# TYPE homedash_checks_total counter
homedash_checks_total{service="Grafana"} 150
```

---

## Admin Endpoints

All admin endpoints require:
- `Authorization: Bearer <API_KEY>` header (if auth enabled)
- `Content-Type: application/json` for POST/PUT/DELETE
- Rate limited: 10 req/s, burst 20

### `GET /admin` — Admin panel
Returns admin dashboard HTML.

---

### `GET /api/admin/groups` — List groups
Returns all configured service groups.

**Response:**
```json
{
  "groups": [
    {
      "name": "Infrastructure",
      "services": [...]
    }
  ]
}
```

---

### `POST /api/admin/group` — Add group
Creates a new service group.

**Request:**
```json
{
  "name": "Media Services"
}
```

**Response:**
```json
{
  "success": true,
  "message": "Group added"
}
```

**Errors:**
- `400` — invalid name, duplicate name
- `500` — save error

---

### `PUT /api/admin/group` — Rename group
Renames an existing group.

**Request:**
```json
{
  "old_name": "Old Name",
  "new_name": "New Name"
}
```

**Response:**
```json
{
  "success": true,
  "message": "Group renamed"
}
```

**Errors:**
- `400` — invalid names, duplicate new name
- `404` — old group not found
- `500` — save error

---

### `DELETE /api/admin/group` — Delete group
Deletes a group and all its services.

**Request:**
```json
{
  "name": "Group to Delete"
}
```

**Response:**
```json
{
  "success": true,
  "message": "Group deleted"
}
```

**Errors:**
- `400` — invalid name
- `404` — group not found
- `500` — save error

---

### `POST /api/admin/service` — Add service
Adds a service to a group.

**Request:**
```json
{
  "group_name": "Infrastructure",
  "service": {
    "name": "Grafana",
    "url": "http://localhost:3000",
    "ip": "127.0.0.1",
    "verify_ssl": false
  }
}
```

**Response:**
```json
{
  "success": true,
  "message": "Service added"
}
```

**Errors:**
- `400` — invalid name/URL/IP, missing required fields
- `404` — group not found
- `500` — save error

---

### `PUT /api/admin/service` — Update service
Updates an existing service.

**Request:**
```json
{
  "group_name": "Infrastructure",
  "old_name": "Grafana",
  "new_service": {
    "name": "Grafana Updated",
    "url": "http://localhost:3001",
    "ip": "127.0.0.1",
    "verify_ssl": true
  }
}
```

**Response:**
```json
{
  "success": true,
  "message": "Service updated"
}
```

---

### `DELETE /api/admin/service` — Delete service
Removes a service from a group.

**Request:**
```json
{
  "group_name": "Infrastructure",
  "service_name": "Grafana"
}
```

**Response:**
```json
{
  "success": true,
  "message": "Service deleted"
}
```

---

### `POST /api/admin/service/move` — Move service
Moves a service from one group to another.

**Request:**
```json
{
  "from_group": "Infrastructure",
  "to_group": "Monitoring",
  "service": "Grafana"
}
```

**Response:**
```json
{
  "success": true,
  "message": "Service moved"
}
```

---

### `POST /api/admin/service/reorder` — Reorder services
Changes the order of services within a group.

**Request:**
```json
{
  "group_name": "Infrastructure",
  "services": ["Grafana", "Nginx", "PostgreSQL"]
}
```

**Response:**
```json
{
  "success": true,
  "message": "Services order updated"
}
```

---

## Middleware Chain

### Public routes
```
CORS → ContentType → MaxBytes (1MB) → Handler
```

### Admin routes
```
CORS → Auth → RateLimit → ContentType → MaxBytes (1MB) → Handler
```

---

## Data Structures

### Service
```json
{
  "name": "string (required)",
  "url": "string (optional, required if ip empty)",
  "ip": "string (optional, required if url empty)",
  "icon": "string (optional, auto-detected)",
  "verify_ssl": "boolean (default: false)"
}
```

### Group
```json
{
  "name": "string (required)",
  "services": "[Service]"
}
```

### Status
```json
{
  "available": "boolean",
  "http": "boolean | null",
  "ping": "boolean | null"
}
```

---

## Error Responses

All errors return plain text (not JSON) with appropriate HTTP status codes:

| Code | Meaning |
|------|---------|
| 400 | Bad request (validation error) |
| 401 | Unauthorized (missing auth header) |
| 403 | Forbidden (invalid API key or auth disabled) |
| 404 | Not found (group/service doesn't exist) |
| 405 | Method not allowed |
| 413 | Payload too large (>1MB) |
| 415 | Unsupported media type (not application/json) |
| 429 | Rate limit exceeded |
| 500 | Internal server error |
