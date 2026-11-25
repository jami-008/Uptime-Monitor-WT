package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestStatusTransitionsAndPersistence(t *testing.T) {
	var failing atomic.Bool
	failing.Store(true)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failing.Load() {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	dbFile := filepath.Join(t.TempDir(), "monitor.json")
	app := newApplication(dbFile)
	site := &Site{ID: "abc", Name: "Test API", URL: target.URL, IntervalSeconds: 5, Status: "unknown"}
	app.sites[site.ID] = site

	app.performCheck(site.ID, site.URL)
	if site.Status != "unknown" {
		t.Fatalf("expected unknown after first failure, got %s", site.Status)
	}
	app.performCheck(site.ID, site.URL)
	if site.Status != "down" {
		t.Fatalf("expected down after second failure, got %s", site.Status)
	}
	if len(app.history) != 2 || app.history[1].StateChange != "down" {
		t.Fatal("expected down event")
	}

	failing.Store(false)
	app.performCheck(site.ID, site.URL)
	if site.Status != "up" {
		t.Fatalf("expected up after recovery, got %s", site.Status)
	}
	if app.history[2].StateChange != "recovered" {
		t.Fatal("expected recovered event")
	}
	if site.TotalChecks != 3 || site.SuccessfulChecks != 1 {
		t.Fatal("check counters incorrect")
	}

	reloaded := newApplication(dbFile)
	if len(reloaded.sites) != 1 || len(reloaded.history) != 3 {
		t.Fatal("persistence was not restored")
	}
	if reloaded.sites[site.ID].Status != "up" {
		t.Fatal("restored site status incorrect")
	}
}

func TestHTTPAPI(t *testing.T) {
	app := newApplication(filepath.Join(t.TempDir(), "data.json"))
	api := httptest.NewServer(app.routes())
	defer api.Close()
	client := api.Client()

	doPost := func(body string) *http.Response {
		t.Helper()
		resp, err := client.Post(api.URL+"/api/sites", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	invalid := doPost(`{"name":"Bad","url":"ftp://example.com","interval_seconds":30}`)
	invalid.Body.Close()
	if invalid.StatusCode != 400 {
		t.Fatalf("expected bad URL to return 400, got %d", invalid.StatusCode)
	}

	success := doPost(`{"name":"Example","url":"https://example.com","interval_seconds":10}`)
	if success.StatusCode != 201 {
		t.Fatalf("expected 201, got %d", success.StatusCode)
	}
	var added Site
	if err := json.NewDecoder(success.Body).Decode(&added); err != nil {
		t.Fatal(err)
	}
	success.Body.Close()
	if added.ID == "" || added.Status != "unknown" {
		t.Fatal("new monitor incorrectly initialized")
	}

	duplicate := doPost(`{"name":"Other","url":"https://example.com","interval_seconds":10}`)
	duplicate.Body.Close()
	if duplicate.StatusCode != 409 {
		t.Fatalf("duplicate expected 409, got %d", duplicate.StatusCode)
	}

	list, err := client.Get(api.URL + "/api/sites")
	if err != nil {
		t.Fatal(err)
	}
	var sites []Site
	if err := json.NewDecoder(list.Body).Decode(&sites); err != nil {
		t.Fatal(err)
	}
	list.Body.Close()
	if len(sites) != 1 {
		t.Fatal("expected 1 monitor in list")
	}

	req, _ := http.NewRequest(http.MethodDelete, api.URL+"/api/sites/"+added.ID, bytes.NewReader(nil))
	deleted, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	deleted.Body.Close()
	if deleted.StatusCode != 204 {
		t.Fatalf("expected 204, got %d", deleted.StatusCode)
	}
}

func TestNormalizeURL(t *testing.T) {
	for _, value := range []string{"javascript:alert(1)", "https://", "https://user:secret@example.com", "not a url", "http://host/#fragment"} {
		if _, err := normalizeURL(value); err == nil {
			t.Errorf("expected invalid URL: %q", value)
		}
	}
	if got, err := normalizeURL(" https://example.com/path?q=1 "); err != nil || got != "https://example.com/path?q=1" {
		t.Fatalf("bad normalization: %q %v", got, err)
	}
}
