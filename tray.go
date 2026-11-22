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
	"strconv"
	"strings"
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
	ep  cliEndpoint
	hc  http.Client
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
	a.ep = ep
	return nil
}

func (a *trayAPI) do(method, path string, out any) error {
	for attempt := 0; attempt < 2; attempt++ {
		if a.ep.Token == "" || attempt > 0 {
			if err := a.load(); err != nil {
				return err
			}
		}
		var body io.Reader
		if method == http.MethodPost {
			body = bytes.NewReader([]byte("{}"))
		}
		req, err := http.NewRequest(method, "http://"+a.ep.Addr+path, body)
		if err != nil {
			return err
		}
		req.Header.Set("X-Encluca", "1")
		req.Header.Set("X-Encluca-Token", a.ep.Token)
		req.Header.Set("Content-Type", "application/json")
		if a.hc.Timeout == 0 {
			a.hc.Timeout = 2 * time.Minute // la chiusura può richiedere tempo (cifratura, invio)
		}
		resp, err := a.hc.Do(req)
		if err != nil {
			a.ep.Token = "" // servizio fermo o riavviato: alla prossima chiamata si rilegge il token
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

// acquireTrayLock garantisce un solo tray per utente: tray.lock contiene il PID del tray
// attivo. Un file con il PID di un processo terminato (uscita brusca) viene sostituito.
func acquireTrayLock(dir string) (release func(), ok bool) {
	p := filepath.Join(dir, "tray.lock")
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			fmt.Fprint(f, os.Getpid())
			f.Close()
			return func() { os.Remove(p) }, true
		}
		b, rerr := os.ReadFile(p)
		pid, perr := strconv.Atoi(strings.TrimSpace(string(b)))
		if rerr == nil && perr == nil && processAlive(pid) {
			return nil, false
		}
		os.Remove(p)
	}
	return nil, false
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
