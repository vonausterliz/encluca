// Encluca: cartella personale cifrata, sincronizzata tramite il NAS e con backup su Google Drive.
package main

import (
	"bytes"
	"context"
	"crypto/subtle"
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

var Version = "0.1.9"

//go:embed web/*.html web/*.css web/*.js web/*.wasm
var webFS embed.FS

//go:embed assets/logo.svg
var logoSVG []byte

// logoURI è il logo come data URI, usato per favicon e marchio nelle pagine.
var logoURI = "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString(logoSVG)

// serveUI invia la pagina con tema (web/theme.css) e traduzioni (web/i18n.js) inclusi al posto dei segnaposto.
// repl sono coppie segnaposto/valore aggiuntive (es. "{{TOKEN}}", token).
func serveUI(w http.ResponseWriter, name string, repl ...string) {
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
	for i := 0; i+1 < len(repl); i += 2 {
		b = bytes.ReplaceAll(b, []byte(repl[i]), []byte(repl[i+1]))
	}
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

Comandi per il client già in esecuzione (opzione comune: --data DIR):
  encluca status [--json]                     stato della cassaforte e della sincronizzazione
  encluca open                                apre la cassaforte (chiede la password; senza terminale la legge da stdin)
  encluca close                               chiude la cassaforte
  encluca sync                                sincronizza subito
  encluca create                              crea la cassaforte (prima volta)
  encluca pair [--name NOME] URL CODICE       collega questo computer al NAS
  encluca confirm-deletes                     conferma le eliminazioni bloccate da inviare al NAS
  encluca confirm-received-deletes            conferma le eliminazioni bloccate ricevute dal NAS
`, Version)
}

func main() {
	// tutto tranne il servizio "client" (che gira senza finestra) scrive sul terminale chiamante
	if len(os.Args) < 2 || os.Args[1] != "client" {
		attachParentConsole()
	}
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
			if errors.Is(err, errUpdateRestart) {
				// systemd/launchd riavviano per il codice ≠ 0; su Windows si rilancia da solo
				exe, _ := os.Executable()
				if c := updateExePath(); c != "" {
					exe = c
				}
				if rerr := relaunchSelf(exe); rerr != nil {
					fmt.Fprintln(os.Stderr, "ERRORE riavvio:", rerr)
				}
				os.Exit(updateExitCode)
			}
			fatal(err)
		}
	default:
		if cliCommands[os.Args[1]] {
			if err := runCLI(os.Args[1], os.Args[2:]); err != nil {
				fatal(err)
			}
			return
		}
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
	uiToken := randHex(32)
	srv := &http.Server{Addr: uiAddr, Handler: securityHeaders(c.uiHandlerToken(uiAddr, uiToken)), ReadHeaderTimeout: 10 * time.Second}
	// dopo un riavvio per aggiornamento la porta può essere ancora del processo precedente
	ln, err := listenRetry(uiAddr, uiListenTries, 500*time.Millisecond)
	if err != nil {
		return err
	}
	// solo l'istanza che ha la porta gestisce l'aggiornamento: un secondo client
	// avviato per errore non deve toccare il pacchetto pronto né il programma
	setupClientUpdater(c, dir)
	if c.upd != nil {
		go c.updateLoop(ctx)
	}
	// solo ora che la porta è nostra: i comandi da terminale (encluca status, open...) usano questo token
	if err := writeCLIEndpoint(dir, uiAddr, uiToken); err != nil {
		c.log.Printf(T("Token per i comandi da terminale non salvato: %v"), err)
	}
	defer os.Remove(cliTokenPath(dir))
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	c.log.Printf(T("Encluca %s avviato: apri http://%s"), Version, uiAddr)
	restart := false
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	case <-c.restart:
		restart = true
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
	if restart {
		return errUpdateRestart
	}
	return nil
}

// setupClientUpdater pulisce i resti di un aggiornamento, registra quello
// appena installato e prepara l'aggiornamento automatico. Un errore qui non
// ferma il client: l'aggiornamento resta solo spento.
func setupClientUpdater(c *Client, dir string) {
	rec := filepath.Join(dir, "update-installed.json")
	var done struct{ From, To string }
	if loadJSON(rec, &done) == nil {
		c.log.Printf(T("Aggiornamento installato: %s → %s"), done.From, done.To)
		os.Remove(rec)
	}
	u, err := newUpdater(dir, Version, c.log.Printf)
	if err != nil {
		c.log.Printf(T("Aggiornamento automatico non disponibile: %v"), err)
		return
	}
	cleanupOldExe(u.exe)
	c.upd = u
	clientExe = u.exe
}

var uiListenTries = 20 // tentativi di ascolto (variabile per i test)

// clientExe è il percorso reale del programma (senza collegamenti): su Windows,
// dopo la sostituzione, os.Executable punterebbe al vecchio file rinominato.
var clientExe string

func updateExePath() string { return clientExe }

func (c *Client) uiHandler(uiAddr string) http.Handler { return c.uiHandlerToken(uiAddr, randHex(32)) }

// uiHandlerToken serve interfaccia e API locale.
// uiToken è il segreto della sessione: è scritto solo nella pagina servita a chi apre
// http://127.0.0.1:8484 (e in cli-token.json, 0600, per i comandi da terminale) e ogni chiamata
// /api/ lo deve rimandare. Un sito web aperto nel browser non può leggere la pagina (stessa
// origine), quindi non può comandare il client neppure se trovasse un modo di aggirare gli altri controlli.
func (c *Client) uiHandlerToken(uiAddr, uiToken string) http.Handler {
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
		On                                            bool
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
	mux.HandleFunc("/api/confirm-recv-deletes", post(func(r *http.Request) (any, error) { c.ConfirmRecvDeletes(); return nil, nil }))
	mux.HandleFunc("/api/workdir", post(func(r *http.Request) (any, error) { return nil, c.SetWorkDir(parse(r).Dir) }))
	mux.HandleFunc("/api/autoupdate", post(func(r *http.Request) (any, error) { return nil, c.SetAutoUpdate(parse(r).On) }))
	mux.HandleFunc("/api/sync", post(func(r *http.Request) (any, error) { return c.SyncNow() }))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		serveUI(w, "web/client.html", "{{TOKEN}}", uiToken)
	})
	// protezione da DNS rebinding: si accettano solo richieste indirizzate a localhost
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowedHosts[strings.ToLower(r.Host)] {
			http.Error(w, "host non ammesso", http.StatusForbidden)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Encluca-Token")), []byte(uiToken)) != 1 {
			// la pagina aperta è di prima di un riavvio del servizio: la UI si ricarica da sola
			writeJSON(w, http.StatusForbidden, map[string]any{"error": T("sessione dell'interfaccia scaduta: ricarica la pagina"), "reload": true})
			return
		}
		mux.ServeHTTP(w, r)
	})
}
