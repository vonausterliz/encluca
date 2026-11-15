package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func withStallTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := updateStallTimeout
	updateStallTimeout = d
	t.Cleanup(func() { updateStallTimeout = old })
}

// Una connessione lenta ma costante non deve essere interrotta, per quanto duri in totale.
func TestDownloadSlowButSteady(t *testing.T) {
	withStallTimeout(t, 300*time.Millisecond)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 10; i++ { // ~1 s in totale, oltre 3 volte il limite di inattività
			w.Write([]byte("0123456789"))
			w.(http.Flusher).Flush()
			time.Sleep(100 * time.Millisecond)
		}
	}))
	defer srv.Close()
	p := filepath.Join(t.TempDir(), "f")
	if _, err := download(t.Context(), srv.Client(), srv.URL, p, 1000); err != nil {
		t.Fatalf("download lento interrotto: %v", err)
	}
	if b, _ := os.ReadFile(p); len(b) != 100 {
		t.Fatalf("scaricati %d byte", len(b))
	}
}

// Una connessione che smette di mandare dati va interrotta presto.
func TestDownloadStallAborts(t *testing.T) {
	withStallTimeout(t, 200*time.Millisecond)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("inizio"))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	start := time.Now()
	_, err := download(t.Context(), srv.Client(), srv.URL, filepath.Join(t.TempDir(), "f"), 1000)
	if err == nil {
		t.Fatal("download bloccato completato")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("interrotto dopo %v", d)
	}
}

// Anche un server che non risponde affatto va abbandonato.
func TestHTTPGetNoResponseAborts(t *testing.T) {
	withStallTimeout(t, 200*time.Millisecond)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	start := time.Now()
	if _, err := readLimited(t.Context(), srv.Client(), srv.URL, 100); err == nil {
		t.Fatal("nessun errore da un server muto")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("interrotto dopo %v", d)
	}
}
