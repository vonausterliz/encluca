package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHubReportsNewerVersion(t *testing.T) {
	h, err := newHub(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	tag := "v9.9.9"
	down := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down {
			http.Error(w, "no", http.StatusServiceUnavailable)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"tag_name": tag, "assets": []any{}})
	}))
	defer srv.Close()
	status := func() map[string]any {
		rec := httptest.NewRecorder()
		h.adminStatus(rec, httptest.NewRequest("GET", "/admin/api/status", nil))
		var m map[string]any
		json.Unmarshal(rec.Body.Bytes(), &m)
		return m
	}
	h.checkLatest(t.Context(), srv.Client(), srv.URL)
	if v := status()["latestVersion"]; v != "9.9.9" {
		t.Fatalf("latestVersion = %v", v)
	}
	tag = "v0.0.1"
	h.checkLatest(t.Context(), srv.Client(), srv.URL)
	if _, ok := status()["latestVersion"]; ok {
		t.Fatal("versione più vecchia segnalata")
	}
	down = true
	logs := func() int {
		n := 0
		for _, l := range h.log.Lines() {
			if strings.Contains(l, "nuove versioni") {
				n++
			}
		}
		return n
	}
	before := logs()
	for i := 0; i < 3; i++ {
		h.checkLatest(t.Context(), srv.Client(), srv.URL)
	}
	if _, ok := status()["latestVersion"]; ok {
		t.Fatal("versione segnalata con l'API non raggiungibile")
	}
	if n := logs() - before; n != 1 {
		t.Fatalf("%d righe di registro per l'API non raggiungibile, attesa 1", n)
	}
}
