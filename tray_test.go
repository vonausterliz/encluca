package main

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Il servizio riavviato cambia token: il tray rilegge cli-token.json e continua.
func TestTrayReloadsToken(t *testing.T) {
	c := newBareClient(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	tokA, tokB := randHex(32), randHex(32)
	var h atomic.Value
	h.Store(c.uiHandlerToken(addr, tokA))
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.Load().(http.Handler).ServeHTTP(w, r) })}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	dir := t.TempDir()
	if err := writeCLIEndpoint(dir, addr, tokA); err != nil {
		t.Fatal(err)
	}
	api := newTrayAPI(dir)
	if _, err := api.status(); err != nil {
		t.Fatalf("primo stato: %v", err)
	}
	h.Store(c.uiHandlerToken(addr, tokB)) // riavvio del servizio: token nuovo
	writeCLIEndpoint(dir, addr, tokB)
	if _, err := api.status(); err != nil {
		t.Fatalf("stato dopo il cambio di token: %v", err)
	}
}

func TestTraySingleInstance(t *testing.T) {
	dir := t.TempDir()
	// avvii simultanei (autostart al login + servizio): uno solo deve vincere
	var wins atomic.Int32
	var rels []func()
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if rel, ok := acquireTrayLock(dir); ok {
				wins.Add(1)
				mu.Lock()
				rels = append(rels, rel)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("tray attivi insieme: %d", wins.Load())
	}
	rels[0]()
	rels[0]() // rilascio ripetuto (rilancio + defer): non deve togliere il blocco di un altro
	rel2, ok := acquireTrayLock(dir)
	if !ok {
		t.Fatal("tray rifiutato dopo il rilascio")
	}
	if _, ok := acquireTrayLock(dir); ok {
		t.Fatal("secondo tray accettato")
	}
	rel2()
	// file rimasto da uno spegnimento brusco, con il PID di un processo qualsiasi (anche vivo)
	os.WriteFile(filepath.Join(dir, "tray.lock"), []byte(strconv.Itoa(os.Getppid())), 0o600)
	if rel, ok := acquireTrayLock(dir); !ok {
		t.Fatal("tray rifiutato per un file di blocco rimasto da un avvio precedente")
	} else {
		rel()
	}
}

// poll, clic sul menu e blocco schermo usano la stessa trayAPI in parallelo (da eseguire con -race)
func TestTrayAPIConcurrent(t *testing.T) {
	c := newBareClient(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tok := randHex(32)
	srv := &http.Server{Handler: c.uiHandlerToken(ln.Addr().String(), tok)}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	dir := t.TempDir()
	writeCLIEndpoint(dir, ln.Addr().String(), tok)
	api := newTrayAPI(dir)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				api.status()
				api.post("/api/autoclose/postpone")
			}
		}()
	}
	wg.Wait()
}

// Blocco dello schermo con un file aperto: il servizio rifiuta (409), il tray non forza e
// avvisa una sola volta anche se i blocchi si ripetono a pochi secondi.
func TestTrayLockCloseRefused(t *testing.T) {
	var closes atomic.Int32
	var bodies []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/status":
			w.Write([]byte(`{"open":true}`))
		case "/api/close":
			closes.Add(1)
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			bodies = append(bodies, string(b))
			mu.Unlock()
			w.WriteHeader(http.StatusConflict)
			w.Write([]byte(`{"error":"non chiudo: 1 file sono aperti in un programma (Q&A.odt)"}`))
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	writeCLIEndpoint(dir, strings.TrimPrefix(srv.URL, "http://"), randHex(32))
	now := time.Now()
	var notes []string
	h := &lockHandler{api: newTrayAPI(dir), now: func() time.Time { return now },
		notify: func(title, body string) { notes = append(notes, title+": "+body) }}
	h.handle()
	now = now.Add(10 * time.Second)
	h.handle()
	if closes.Load() != 2 {
		t.Fatalf("tentativi di chiusura: %d", closes.Load())
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "Q&A.odt") {
		t.Fatalf("notifiche: %q", notes)
	}
	for _, b := range bodies {
		if strings.Contains(strings.ToLower(b), "force") {
			t.Fatalf("chiusura forzata: %s", b)
		}
	}
	now = now.Add(2 * time.Minute)
	h.handle()
	if len(notes) != 2 {
		t.Fatalf("dopo un minuto il rifiuto va notificato di nuovo: %q", notes)
	}
}
