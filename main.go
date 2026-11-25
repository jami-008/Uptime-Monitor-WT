package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	defaultInterval  = 30
	minInterval      = 5
	maxInterval      = 3600
	failureThreshold = 2
	maxHistory       = 300
	requestTimeout   = 8 * time.Second
)

//go:embed web/*
var webFiles embed.FS

type Site struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	URL              string    `json:"url"`
	IntervalSeconds  int       `json:"interval_seconds"`
	Status           string    `json:"status"` // unknown, up, down
	LastCheckedAt    time.Time `json:"last_checked_at,omitempty"`
	NextCheckAt      time.Time `json:"next_check_at,omitempty"`
	ResponseTimeMs   int64     `json:"response_time_ms"`
	HTTPStatus       int       `json:"http_status"`
	FailureStreak    int       `json:"failure_streak"`
	TotalChecks      int64     `json:"total_checks"`
	SuccessfulChecks int64     `json:"successful_checks"`
	LastError        string    `json:"last_error,omitempty"`
	DownSince        time.Time `json:"down_since,omitempty"`
	Checking         bool      `json:"checking"`
}

type CheckEvent struct {
	At             time.Time `json:"at"`
	SiteID         string    `json:"site_id"`
	SiteName       string    `json:"site_name"`
	URL            string    `json:"url"`
	OK             bool      `json:"ok"`
	HTTPStatus     int       `json:"http_status"`
	ResponseTimeMs int64     `json:"response_time_ms"`
	Error          string    `json:"error,omitempty"`
	StateChange    string    `json:"state_change,omitempty"`
}

type storedData struct {
	Sites   []*Site      `json:"sites"`
	History []CheckEvent `json:"history"`
}

type application struct {
	mu             sync.RWMutex
	sites          map[string]*Site
	history        []CheckEvent
	filePath       string
	client         *http.Client
	telegramToken  string
	telegramChatID string
}

func newApplication(filePath string) *application {
	a := &application{
		sites:          make(map[string]*Site),
		history:        make([]CheckEvent, 0),
		filePath:       filePath,
		client:         &http.Client{Timeout: requestTimeout},
		telegramToken:  strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN")),
		telegramChatID: strings.TrimSpace(os.Getenv("TELEGRAM_CHAT_ID")),
	}
	if err := a.load(); err != nil {
		log.Printf("Could not load saved data: %v", err)
	}
	return a
}

func (a *application) load() error {
	raw, err := os.ReadFile(a.filePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var data storedData
	if err := json.Unmarshal(raw, &data); err != nil {
		return fmt.Errorf("decode data file: %w", err)
	}
	for _, s := range data.Sites {
		if s.ID == "" || s.URL == "" {
			continue
		}
		s.Checking = false
		s.NextCheckAt = time.Now() // Check immediately after restart.
		a.sites[s.ID] = s
	}
	a.history = data.History
	if len(a.history) > maxHistory {
		a.history = a.history[len(a.history)-maxHistory:]
	}
	return nil
}

// saveLocked is called with a.mu held, so older data cannot overwrite newer data.
func (a *application) saveLocked() error {
	sites := make([]*Site, 0, len(a.sites))
	for _, s := range a.sites {
		sites = append(sites, s)
	}
	sort.Slice(sites, func(i, j int) bool { return sites[i].ID < sites[j].ID })
	raw, err := json.MarshalIndent(storedData{Sites: sites, History: a.history}, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(a.filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".uptime-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), a.filePath)
}

func (a *application) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/sites", a.sitesHandler)
	mux.HandleFunc("/api/sites/", a.siteActionHandler)
	mux.HandleFunc("/api/history", a.historyHandler)
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "time": time.Now().UTC()})
	})
	sub, _ := fs.Sub(webFiles, "web")
	mux.Handle("/", http.FileServer(http.FS(sub)))
	return mux
}

func (a *application) sitesHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.mu.RLock()
		sites := make([]Site, 0, len(a.sites))
		for _, s := range a.sites {
			sites = append(sites, *s)
		}
		a.mu.RUnlock()
		sort.Slice(sites, func(i, j int) bool { return strings.ToLower(sites[i].Name) < strings.ToLower(sites[j].Name) })
		writeJSON(w, http.StatusOK, sites)
	case http.MethodPost:
		var input struct {
			Name            string `json:"name"`
			URL             string `json:"url"`
			IntervalSeconds int    `json:"interval_seconds"`
		}
		if err := decodeJSON(w, r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		input.Name = strings.TrimSpace(input.Name)
		normalized, err := normalizeURL(input.URL)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if input.Name == "" {
			parsed, _ := url.Parse(normalized)
			input.Name = parsed.Hostname()
		}
		if len(input.Name) > 80 {
			writeError(w, http.StatusBadRequest, "Name must be 80 characters or fewer")
			return
		}
		if input.IntervalSeconds == 0 {
			input.IntervalSeconds = defaultInterval
		}
		if input.IntervalSeconds < minInterval || input.IntervalSeconds > maxInterval {
			writeError(w, http.StatusBadRequest, "Interval must be between 5 and 3600 seconds")
			return
		}
		id, err := newID()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Could not generate site ID")
			return
		}
		site := &Site{ID: id, Name: input.Name, URL: normalized, IntervalSeconds: input.IntervalSeconds, Status: "unknown", NextCheckAt: time.Now()}
		a.mu.Lock()
		for _, existing := range a.sites {
			if existing.URL == normalized {
				a.mu.Unlock()
				writeError(w, http.StatusConflict, "This URL is already being monitored")
				return
			}
		}
		a.sites[id] = site
		if err := a.saveLocked(); err != nil {
			log.Printf("Save error: %v", err)
		}
		copy := *site
		a.mu.Unlock()
		writeJSON(w, http.StatusCreated, copy)
		// The scheduler will check it within one second.
	default:
		methodNotAllowed(w)
	}
}

func (a *application) siteActionHandler(w http.ResponseWriter, r *http.Request) {
	p := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/sites/"), "/")
	parts := strings.Split(p, "/")
	if len(parts) == 1 && parts[0] != "" && r.Method == http.MethodDelete {
		a.mu.Lock()
		if _, ok := a.sites[parts[0]]; !ok {
			a.mu.Unlock()
			writeError(w, http.StatusNotFound, "Site not found")
			return
		}
		delete(a.sites, parts[0])
		if err := a.saveLocked(); err != nil {
			log.Printf("Save error: %v", err)
		}
		a.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if len(parts) == 2 && parts[0] != "" && parts[1] == "check" && r.Method == http.MethodPost {
		if !a.startCheck(parts[0]) {
			a.mu.RLock()
			_, exists := a.sites[parts[0]]
			a.mu.RUnlock()
			if !exists {
				writeError(w, http.StatusNotFound, "Site not found")
			} else {
				writeError(w, http.StatusConflict, "A check is already running")
			}
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"message": "Check started"})
		return
	}
	writeError(w, http.StatusNotFound, "Unknown endpoint or method")
}

func (a *application) historyHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	a.mu.RLock()
	start := len(a.history) - 80
	if start < 0 {
		start = 0
	}
	events := make([]CheckEvent, 0, len(a.history)-start)
	for i := len(a.history) - 1; i >= start; i-- {
		events = append(events, a.history[i])
	}
	a.mu.RUnlock()
	writeJSON(w, http.StatusOK, events)
}

func (a *application) startCheck(id string) bool {
	a.mu.Lock()
	s, ok := a.sites[id]
	if !ok || s.Checking {
		a.mu.Unlock()
		return false
	}
	s.Checking = true
	s.NextCheckAt = time.Now().Add(time.Duration(s.IntervalSeconds) * time.Second)
	targetURL := s.URL
	a.mu.Unlock()
	go a.performCheck(id, targetURL)
	return true
}

func (a *application) runScheduler(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	// Check immediately on startup (and then every second).
	schedule := func() {
		a.mu.RLock()
		due := make([]string, 0)
		now := time.Now()
		for id, site := range a.sites {
			if !site.Checking && !now.Before(site.NextCheckAt) {
				due = append(due, id)
			}
		}
		a.mu.RUnlock()
		for _, id := range due {
			a.startCheck(id)
		}
	}
	schedule()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			schedule()
		}
	}
}

func (a *application) performCheck(id, targetURL string) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	code := 0
	ok := false
	errorMessage := ""
	if err == nil {
		req.Header.Set("User-Agent", "PulseWatch-Go-Monitor/1.0")
		req.Header.Set("Cache-Control", "no-cache")
		resp, reqErr := a.client.Do(req)
		if reqErr != nil {
			err = reqErr
		} else {
			code = resp.StatusCode
			ok = code >= 200 && code < 400
			// Do not download full pages, video, or other large response bodies.
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
			resp.Body.Close()
			if !ok {
				errorMessage = fmt.Sprintf("HTTP %d %s", code, http.StatusText(code))
			}
		}
	}
	if err != nil {
		errorMessage = cleanError(err)
	}
	ms := time.Since(start).Milliseconds()
	now := time.Now()

	a.mu.Lock()
	site, exists := a.sites[id]
	if !exists {
		a.mu.Unlock()
		return
	} // Deleted during check.
	previous := site.Status
	site.Checking = false
	site.LastCheckedAt = now
	site.ResponseTimeMs = ms
	site.HTTPStatus = code
	site.TotalChecks++
	site.LastError = errorMessage
	stateChange := ""
	if ok {
		site.SuccessfulChecks++
		site.FailureStreak = 0
		site.Status = "up"
		site.DownSince = time.Time{}
		if previous == "down" {
			stateChange = "recovered"
		}
	} else {
		site.FailureStreak++
		if site.FailureStreak >= failureThreshold {
			site.Status = "down"
			if previous != "down" {
				stateChange = "down"
				site.DownSince = now
			}
		}
	}
	a.history = append(a.history, CheckEvent{
		At: now, SiteID: id, SiteName: site.Name, URL: site.URL,
		OK: ok, HTTPStatus: code, ResponseTimeMs: ms, Error: errorMessage,
		StateChange: stateChange,
	})
	if len(a.history) > maxHistory {
		a.history = a.history[len(a.history)-maxHistory:]
	}
	name, siteURL := site.Name, site.URL
	if err := a.saveLocked(); err != nil {
		log.Printf("Save error: %v", err)
	}
	a.mu.Unlock()
	if stateChange == "down" {
		go a.notify(fmt.Sprintf("🔴 DOWN: %s\n%s\nReason: %s\nChecked: %s", name, siteURL, errorMessage, now.Format(time.RFC3339)))
	} else if stateChange == "recovered" {
		go a.notify(fmt.Sprintf("🟢 RECOVERED: %s\n%s\nResponse time: %d ms\nChecked: %s", name, siteURL, ms, now.Format(time.RFC3339)))
	}
}

func (a *application) notify(message string) {
	log.Printf("[STATUS ALERT] %s", strings.ReplaceAll(message, "\n", " | "))
	if a.telegramToken == "" || a.telegramChatID == "" {
		return
	}
	endpoint := "https://api.telegram.org/bot" + a.telegramToken + "/sendMessage"
	values := url.Values{"chat_id": {a.telegramChatID}, "text": {message}}
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		log.Printf("Telegram request error: %s", strings.ReplaceAll(err.Error(), a.telegramToken, "[REDACTED]"))
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := a.client.Do(req)
	if err != nil {
		log.Printf("Telegram error: %s", strings.ReplaceAll(err.Error(), a.telegramToken, "[REDACTED]"))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("Telegram returned HTTP %d (check bot token and chat ID)", resp.StatusCode)
	}
}

func normalizeURL(input string) (string, error) {
	input = strings.TrimSpace(input)
	if len(input) > 2048 {
		return "", errors.New("URL is too long")
	}
	u, err := url.Parse(input)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", errors.New("Enter a full http:// or https:// URL")
	}
	if u.User != nil {
		return "", errors.New("URLs with embedded usernames/passwords are not allowed")
	}
	if u.Fragment != "" {
		return "", errors.New("URL fragments are not supported")
	}
	if strings.ContainsAny(u.Host, " \n\r\t") {
		return "", errors.New("Invalid URL hostname")
	}
	return u.String(), nil
}

func newID() (string, error) {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func cleanError(err error) string {
	// Avoid displaying extremely long library or proxy errors in the UI.
	value := err.Error()
	if len(value) > 220 {
		return value[:220] + "…"
	}
	return value
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("request must contain exactly one JSON object")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
func methodNotAllowed(w http.ResponseWriter) {
	writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
}

func main() {
	port := strings.TrimSpace(os.Getenv("PORT"))
	if port == "" {
		port = "8080"
	}
	if _, err := strconv.Atoi(port); err != nil {
		log.Fatal("PORT must be numeric")
	}
	addr := "127.0.0.1:" + port // Local-only by default; no authentication in this demo.
	if os.Getenv("BIND_ALL") == "1" {
		addr = ":" + port
	}
	path := os.Getenv("DATA_FILE")
	if path == "" {
		path = "data/uptime.json"
	}
	app := newApplication(path)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go app.runScheduler(ctx)

	server := &http.Server{Addr: addr, Handler: app.routes(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Printf("PulseWatch running at http://localhost:%s", port)
		log.Printf("Checks: every site's interval; down after %d consecutive failures", failureThreshold)
		if app.telegramToken == "" || app.telegramChatID == "" {
			log.Printf("Telegram alerts disabled (set TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID to enable)")
		}
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownCtx)
}
