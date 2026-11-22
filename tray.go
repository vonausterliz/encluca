package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Processo "encluca tray": icona nella barra di sistema. È un client dell'API locale come i
// comandi da terminale (cli-token.json), quindi il modello di sicurezza non cambia.

const (
	trayPoll         = 2 * time.Second
	trayRefusalEvery = 60 * time.Second // al massimo una notifica di chiusura rifiutata al minuto
)

// trayAPI chiama il servizio locale. Dopo un rifiuto per token (riavvio del servizio)
// rilegge cli-token.json e riprova una volta.
type trayAPI struct {
	dir string
	hc  http.Client
	mu  sync.Mutex // ep è letto e riscritto da poll, menu e blocco schermo in parallelo
	ep  cliEndpoint
}

func (a *trayAPI) load() error {
	b, err := os.ReadFile(cliTokenPath(a.dir))
	if err != nil {
		return err
	}
	var ep cliEndpoint
	if err := json.Unmarshal(b, &ep); err != nil || ep.Addr == "" || ep.Token == "" {
		return errors.New(T("file del token non valido: riavvia il servizio encluca client"))
	}
	a.mu.Lock()
	a.ep = ep
	a.mu.Unlock()
	return nil
}

func (a *trayAPI) endpoint() cliEndpoint {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.ep
}

func (a *trayAPI) forget() {
	a.mu.Lock()
	a.ep.Token = ""
	a.mu.Unlock()
}

func (a *trayAPI) do(method, path string, out any) error {
	for attempt := 0; attempt < 2; attempt++ {
		if a.endpoint().Token == "" || attempt > 0 {
			if err := a.load(); err != nil {
				return err
			}
		}
		ep := a.endpoint()
		var body io.Reader
		if method == http.MethodPost {
			body = bytes.NewReader([]byte("{}"))
		}
		req, err := http.NewRequest(method, "http://"+ep.Addr+path, body)
		if err != nil {
			return err
		}
		req.Header.Set("X-Encluca", "1")
		req.Header.Set("X-Encluca-Token", ep.Token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := a.hc.Do(req)
		if err != nil {
			a.forget() // servizio fermo o riavviato: alla prossima chiamata si rilegge il token
			return err
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
			continue
		}
		if resp.StatusCode != http.StatusOK {
			var e struct {
				Error string `json:"error"`
			}
			json.Unmarshal(raw, &e)
			if e.Error == "" {
				e.Error = resp.Status
			}
			return errors.New(e.Error)
		}
		if out != nil {
			return json.Unmarshal(raw, out)
		}
		return nil
	}
	return errors.New(T("token non valido: riavvia il servizio encluca client"))
}

func (a *trayAPI) status() (trayStatus, error) {
	var st trayStatus
	err := a.do(http.MethodGet, "/api/status", &st)
	return st, err
}

func (a *trayAPI) post(path string) error { return a.do(http.MethodPost, path, nil) }

// acquireTrayLock garantisce un solo tray per utente con un blocco del sistema operativo su
// tray.lock (flock / LockFileEx): sparisce da solo quando il processo termina, quindi niente
// file orfani né PID riusati. release è idempotente.
func acquireTrayLock(dir string) (release func(), ok bool) {
	f, err := os.OpenFile(filepath.Join(dir, "tray.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, false
	}
	if lockFileExclusive(f) != nil {
		f.Close()
		return nil, false
	}
	f.Truncate(0)
	fmt.Fprint(f, os.Getpid()) // solo informativo
	var once sync.Once
	return func() { once.Do(func() { f.Close() }) }, true
}

// trayLog aggiunge una riga a tray.log nella cartella dati (il tray non ha una console).
func trayLog(dir, format string, a ...any) {
	p := filepath.Join(dir, "tray.log")
	if fi, err := os.Stat(p); err == nil && fi.Size() > 1<<20 {
		os.Rename(p, p+".1")
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, a...))
}

// lockHandler reagisce al blocco dello schermo o alla sospensione: chiude se serve; se il
// servizio rifiuta (file aperti, eliminazioni in attesa…) non forza e avvisa, al massimo
// una volta ogni trayRefusalEvery. Serializzato: eventi vicini (blocco + sospensione) non
// fanno due chiusure in parallelo.
type lockHandler struct {
	api    *trayAPI
	now    func() time.Time
	notify func(title, body string)
	mu     sync.Mutex
	last   time.Time
}

func (h *lockHandler) handle() {
	h.mu.Lock()
	defer h.mu.Unlock()
	err := trayLockedClose(h.api)
	if err == nil {
		return
	}
	if !h.last.IsZero() && h.now().Sub(h.last) < trayRefusalEvery {
		return
	}
	h.last = h.now()
	h.notify(T("Cassaforte non chiusa"), err.Error())
}

// trayLockedClose è la reazione al blocco dello schermo o alla sospensione. Restituisce
// l'errore della chiusura (rifiutata: file aperti, eliminazioni in attesa…) o nil.
func trayLockedClose(api *trayAPI) error {
	st, err := api.status()
	if err != nil || !lockAction(trayState{Reachable: true, Status: st}) {
		return nil
	}
	return api.post("/api/close")
}

// la chiusura può richiedere tempo (cifratura, invio al NAS)
func newTrayAPI(dir string) *trayAPI {
	return &trayAPI{dir: dir, hc: http.Client{Timeout: 2 * time.Minute}}
}
