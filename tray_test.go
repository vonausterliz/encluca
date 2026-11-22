package main

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
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
	api := &trayAPI{dir: dir}
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
	rel1, ok := acquireTrayLock(dir)
	if !ok {
		t.Fatal("primo tray rifiutato")
	}
	if _, ok := acquireTrayLock(dir); ok {
		t.Fatal("secondo tray accettato mentre il primo è attivo")
	}
	rel1()
	rel3, ok := acquireTrayLock(dir)
	if !ok {
		t.Fatal("tray rifiutato dopo il rilascio")
	}
	rel3()
	// PID di un processo inesistente: il file è rimasto da un'uscita brusca
	os.WriteFile(filepath.Join(dir, "tray.lock"), []byte(strconv.Itoa(1<<22+12345)), 0o600)
	if rel, ok := acquireTrayLock(dir); !ok {
		t.Fatal("tray rifiutato con un file di blocco orfano")
	} else {
		rel()
	}
}
