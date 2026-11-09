// Encluca: cartella personale cifrata, sincronizzata tramite il NAS e con backup su Google Drive.
package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

var Version = "0.1.6"

//go:embed web/*.html web/*.css web/*.js web/*.wasm
var webFS embed.FS

//go:embed assets/logo.svg
var logoSVG []byte

// logoURI è il logo come data URI, usato per favicon e marchio nelle pagine.
var logoURI = "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString(logoSVG)

// serveUI invia la pagina con tema (web/theme.css) e traduzioni (web/i18n.js) inclusi al posto dei segnaposto.
func serveUI(w http.ResponseWriter, name string) {
	b, err := webFS.ReadFile(name)
	css, err2 := webFS.ReadFile("web/theme.css")
	js, err3 := webFS.ReadFile("web/i18n.js")
	if err != nil || err2 != nil || err3 != nil {
		http.Error(w, "interfaccia non trovata", 500)
		return
	}
	b = bytes.Replace(b, []byte("/*THEME*/"), css, 1)
	b = bytes.Replace(b, []byte("/*I18N*/"), js, 1)
	b = bytes.ReplaceAll(b, []byte("{{LOGO}}"), []byte(logoURI))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(b)
}

func usage() {
	fmt.Fprintf(os.Stderr, `Encluca %s

Uso:
  encluca client [--data DIR] [--ui 127.0.0.1:8484]   servizio sul PC (interfaccia su http://127.0.0.1:8484)
  encluca hub    [--data DIR] [--listen :8443]        servizio sul NAS (interfaccia su https://NAS:8443)
  encluca hub-reset-password [--data DIR]             genera una nuova password amministratore dell'hub
  encluca version
`, Version)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch os.Args[1] {
	case "version", "--version", "-v":
		fmt.Println("Encluca", Version)
	case "hub":
		fs := flag.NewFlagSet("hub", flag.ExitOnError)
		data := fs.String("data", envOr("ENCLUCA_DATA", "/data"), "cartella dati dell'hub")
		listen := fs.String("listen", envOr("ENCLUCA_LISTEN", ":8443"), "indirizzo di ascolto")
		fs.Parse(os.Args[2:])
		h, err := newHub(*data, *listen)
		if err != nil {
			fatal(err)
		}
		if err := h.run(ctx); err != nil {
			fatal(err)
		}
	case "hub-reset-password":
		fs := flag.NewFlagSet("hub-reset-password", flag.ExitOnError)
		data := fs.String("data", envOr("ENCLUCA_DATA", "/data"), "cartella dati dell'hub")
		fs.Parse(os.Args[2:])
		h, err := newHub(*data, "")
		if err != nil {
			fatal(err)
		}
		pw := randPassword(16)
		h.setAdminPassword(pw)
		if err := h.saveCfg(); err != nil {
			fatal(err)
		}
		os.WriteFile(filepath.Join(*data, "admin-password.txt"), []byte(pw+"\n"), 0o600)
		fmt.Println("Nuova password amministratore:", pw)
		fmt.Println("Riavvia il servizio hub per applicarla.")
	case "client":
		fs := flag.NewFlagSet("client", flag.ExitOnError)
		data := fs.String("data", envOr("ENCLUCA_DATA", defaultClientDir()), "cartella dati del client")
		ui := fs.String("ui", envOr("ENCLUCA_UI", "127.0.0.1:8484"), "indirizzo dell'interfaccia (solo locale)")
		fs.Parse(os.Args[2:])
		if err := runClient(ctx, *data, *ui); err != nil {
			fatal(err)
		}
	default:
		usage()
		os.Exit(2)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "ERRORE:", err)
	os.Exit(1)
}

func runClient(ctx context.Context, dir, uiAddr string) error {
	host, _, err := net.SplitHostPort(uiAddr)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return errors.New(T("l'interfaccia del client può ascoltare solo su 127.0.0.1/localhost"))
	}
	c, err := newClient(dir)
	if err != nil {
		return err
	}
	c.run(ctx)
	srv := &http.Server{Addr: uiAddr, Handler: securityHeaders(c.uiHandler(uiAddr)), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	c.log.Printf(T("Encluca %s avviato: apri http://%s"), Version, uiAddr)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	// arresto ordinato (spegnimento del PC, stop del servizio): se aperta, chiude la cassaforte
	c.mu.Lock()
	open := c.cip != nil
	c.mu.Unlock()
	if open {
		c.log.Print(T("Arresto del servizio: chiudo la cassaforte"))
		if err := c.Close(); err != nil {
			c.log.Printf(T("Chiusura all'arresto non riuscita: %v"), err)
		}
	}
	sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	srv.Shutdown(sctx)
	return nil
}

func (c *Client) uiHandler(uiAddr string) http.Handler {
	_, port, _ := net.SplitHostPort(uiAddr)
	allowedHosts := map[string]bool{"127.0.0.1:" + port: true, "localhost:" + port: true, "[::1]:" + port: true}
	mux := http.NewServeMux()
	post := func(fn func(r *http.Request) (any, error)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.Header.Get("X-Encluca") != "1" {
				writeErr(w, http.StatusForbidden, T("richiesta non valida"))
				return
			}
			res, err := fn(r)
			if err != nil {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			if res == nil {
				res = map[string]bool{"ok": true}
			}
			writeJSON(w, http.StatusOK, res)
		}
	}
	type body struct {
		Password, Code, HubURL, Name, Dir, Path, Lang string
	}
	parse := func(r *http.Request) body {
		var b body
		readBody(r, &b)
		return b
	}
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, c.Status()) })
	mux.HandleFunc("/api/pair", post(func(r *http.Request) (any, error) {
		b := parse(r)
		return nil, c.Pair(b.Code, b.HubURL, b.Name)
	}))
	mux.HandleFunc("/api/create", post(func(r *http.Request) (any, error) { return nil, c.CreateVault(parse(r).Password) }))
	mux.HandleFunc("/api/open", post(func(r *http.Request) (any, error) { return nil, c.Open(parse(r).Password) }))
	mux.HandleFunc("/api/close", post(func(r *http.Request) (any, error) { return nil, c.Close() }))
	mux.HandleFunc("/api/openfolder", post(func(r *http.Request) (any, error) { return nil, c.OpenFolder() }))
	mux.HandleFunc("/api/lang", post(func(r *http.Request) (any, error) { return nil, c.SetLang(parse(r).Lang) }))
	mux.HandleFunc("/api/rename", post(func(r *http.Request) (any, error) { b := parse(r); return nil, c.Rename(b.Path, b.Name) }))
	mux.HandleFunc("/api/confirm-deletes", post(func(r *http.Request) (any, error) { c.ConfirmDeletes(); return nil, nil }))
	mux.HandleFunc("/api/workdir", post(func(r *http.Request) (any, error) { return nil, c.SetWorkDir(parse(r).Dir) }))
	mux.HandleFunc("/api/sync", post(func(r *http.Request) (any, error) { return c.SyncNow() }))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		serveUI(w, "web/client.html")
	})
	// protezione da DNS rebinding: si accettano solo richieste indirizzate a localhost
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowedHosts[strings.ToLower(r.Host)] {
			http.Error(w, "host non ammesso", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})
}
