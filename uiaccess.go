package main

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"html"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Accesso all'interfaccia locale.
//
// L'interfaccia ascolta su 127.0.0.1, raggiungibile da qualunque utente dello stesso PC. Il
// token che comanda la cassaforte è scritto nella pagina, quindi la pagina va consegnata solo
// all'utente del servizio. Non potendo sapere chi c'è dall'altra parte di una connessione
// locale, si usa quello che solo quell'utente può fare: leggere un suo file.
//
//  1. Chi apre la pagina senza sessione riceve una pagina "bloccata", senza token, che chiede
//     al servizio di sbloccare (POST /unlock).
//  2. Il servizio scrive un biglietto monouso in un file leggibile solo dall'utente e lo apre
//     nel browser dell'utente (quello della sua sessione grafica). Il biglietto non passa mai
//     dalla riga di comando, che gli altri utenti possono vedere.
//  3. Il file porta il browser su /?ticket=…: il servizio scambia il biglietto con un cookie
//     di sessione. Un altro utente che chiede lo sblocco fa solo aprire una scheda nel browser
//     dell'utente giusto, senza ottenere nulla.
//
// I comandi da terminale non passano di qui: leggono il token da cli-token.json (0600).

const (
	uiCookieName   = "el_ui"
	uiCookieMaxAge = 90 * 24 * 3600
	ticketValidity = 60 * time.Second
	unlockInterval = 5 * time.Second
	unlockFileName = "Encluca - accesso.html"
)

// openInBrowser apre un file nel browser predefinito dell'utente del servizio.
var openInBrowser = func(path string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", path).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", path).Start()
	default:
		return exec.Command("xdg-open", path).Start()
	}
}

// unlockFileDir è la cartella del file di accesso: la home dell'utente, perché i browser
// installati come pacchetti isolati (snap, flatpak) non leggono le cartelle nascoste.
var unlockFileDir = func() string {
	home, _ := os.UserHomeDir()
	return home
}

// loadUICookie restituisce il segreto della sessione dell'interfaccia, creandolo la prima
// volta. È conservato (0600) così la sessione del browser sopravvive ai riavvii del servizio.
func loadUICookie(dir string) string {
	p := filepath.Join(dir, "ui-cookie.key")
	if b, err := os.ReadFile(p); err == nil {
		if s := strings.TrimSpace(string(b)); len(s) == 64 {
			return s
		}
	}
	s := randHex(32)
	writeFileAtomic(p, []byte(s+"\n"), 0o600)
	return s
}

type uiAccess struct {
	uiAddr string
	cookie string
	logf   func(string, ...any)

	mu         sync.Mutex
	tickets    map[string]ticket
	lastUnlock time.Time
}

type ticket struct {
	exp  time.Time
	file string
}

func newUIAccess(dir, uiAddr string, logf func(string, ...any)) *uiAccess {
	return &uiAccess{uiAddr: uiAddr, cookie: loadUICookie(dir), logf: logf, tickets: map[string]ticket{}}
}

func (a *uiAccess) hasSession(r *http.Request) bool {
	c, err := r.Cookie(uiCookieName)
	return err == nil && subtle.ConstantTimeCompare([]byte(c.Value), []byte(a.cookie)) == 1
}

// unlock prepara un biglietto e lo apre nel browser dell'utente del servizio.
func (a *uiAccess) unlock() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	for k, t := range a.tickets {
		if now.After(t.exp) {
			os.Remove(t.file)
			delete(a.tickets, k)
		}
	}
	if now.Sub(a.lastUnlock) < unlockInterval {
		return nil // richiesta ravvicinata: la scheda è già stata aperta
	}
	dir := unlockFileDir()
	if dir == "" {
		return errors.New(T("cartella dell'utente non trovata"))
	}
	file := filepath.Join(dir, unlockFileName)
	os.Remove(file)
	// O_EXCL: non si scrive mai attraverso un file o un collegamento messo lì da altri
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	tk := randHex(32)
	url := "http://" + a.uiAddr + "/?ticket=" + tk
	_, werr := fmt.Fprintf(f, "<!doctype html><meta charset=\"utf-8\"><title>Encluca</title><meta http-equiv=\"refresh\" content=\"0;url=%s\"><p><a href=\"%s\">Encluca</a></p>\n", url, url)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(file)
		return werr
	}
	if err := openInBrowser(file); err != nil {
		os.Remove(file)
		return err
	}
	a.tickets[tk] = ticket{exp: now.Add(ticketValidity), file: file}
	a.lastUnlock = now
	time.AfterFunc(ticketValidity, func() { os.Remove(file) })
	return nil
}

// redeem consuma un biglietto: vale una volta sola ed entro ticketValidity.
func (a *uiAccess) redeem(tk string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	t, ok := a.tickets[tk]
	if !ok {
		return false
	}
	delete(a.tickets, tk)
	os.Remove(t.file)
	return time.Now().Before(t.exp)
}

func (a *uiAccess) setSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: uiCookieName, Value: a.cookie, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: uiCookieMaxAge})
}

func htmlPage(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Encluca</title>
<style>body{margin:0;min-height:100vh;display:grid;place-items:center;background:#1E1E1E;color:#D6D6D6;font:15px/1.5 system-ui,sans-serif}main{max-width:460px;padding:24px;text-align:center}h1{color:#fff;font-size:22px;font-weight:500}code{background:#383838;padding:1px 6px;border-radius:4px}</style>
</head><body><main><h1>Encluca</h1>%s</main></body></html>`, body)
}

// serveLocked è la pagina per chi non ha (ancora) la sessione: non contiene il token.
func (a *uiAccess) serveLocked(w http.ResponseWriter) {
	e := html.EscapeString
	htmlPage(w, fmt.Sprintf(`<p id="m">%s</p>
<script>
fetch('/unlock',{method:'POST',headers:{'X-Encluca':'1'}}).then(r=>r.json().then(j=>({ok:r.ok,j}))).then(x=>{
  document.getElementById('m').textContent = x.ok ? %q : %q + (x.j.error ? ' (' + x.j.error + ')' : '');
}).catch(()=>{ document.getElementById('m').textContent = %q; });
</script>`,
		e(T("Per sicurezza Encluca si apre solo dal tuo utente: sto aprendo una nuova scheda…")),
		T("Encluca si è aperto in una nuova scheda del browser: puoi chiudere questa."),
		T("Non riesco ad aprire il browser da qui. Dal terminale puoi usare i comandi: encluca status, encluca open, encluca close."),
		T("Non riesco ad aprire il browser da qui. Dal terminale puoi usare i comandi: encluca status, encluca open, encluca close.")))
}

// serveEntered conclude lo scambio del biglietto. Niente reindirizzamento HTTP: il cookie è
// SameSite=Strict e non verrebbe inviato in una catena partita da un file locale; la pagina
// stessa passa all'interfaccia, e da lì il cookie vale.
func (a *uiAccess) serveEntered(w http.ResponseWriter) {
	a.setSession(w)
	htmlPage(w, `<script>location.replace('/')</script>`)
}
