package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// GDriveConfig è salvata nella config dell'hub. Lo scope usato è drive.file:
// l'app vede solo i file che ha creato lei, non il resto del tuo Drive.
type GDriveConfig struct {
	ClientID      string `json:"clientId"`
	ClientSecret  string `json:"clientSecret"`
	RefreshToken  string `json:"refreshToken"`
	FolderName    string `json:"folderName"`
	RetentionDays int    `json:"retentionDays"`
	IntervalMin   int    `json:"intervalMin"`
}

type gdFile struct {
	ID     string `json:"id"`
	SHA    string `json:"sha"`
	Parent string `json:"parent"`
}

type gdState struct {
	RootID     string             `json:"rootId"`
	VaultID    string             `json:"vaultId"`
	VersionsID string             `json:"versionsId"`
	MetaID     string             `json:"metaId"`
	MetaSHA    string             `json:"metaSha"`
	ReadmeID   string             `json:"readmeId"`
	Folders    map[string]string  `json:"folders"`
	Files      map[string]*gdFile `json:"files"`
	// Dirs: cartelle vuote della cassaforte già create su Drive (l'ID è in Folders)
	Dirs map[string]bool `json:"dirs,omitempty"`
	// Clean è vero se l'ultimo giro è terminato regolarmente. Se è falso (arresto brusco),
	// alcuni file potrebbero essere stati caricati senza essere registrati: il giro
	// successivo controlla su Drive prima di caricare, per non creare doppioni.
	Clean bool `json:"clean"`
	// ultimo backup completato, conservato anche dopo i riavvii
	LastOK   time.Time `json:"lastOk,omitempty"`
	LastInfo string    `json:"lastInfo,omitempty"`
}

type GDrive struct {
	h         *Hub
	mu        sync.Mutex
	st        gdState
	access    string
	expiry    time.Time
	dirty     bool
	lastChg   time.Time
	trigger   chan struct{}
	running   bool
	prog      *progress // avanzamento del backup in corso (nil se fermo)
	lastRun   time.Time
	lastOK    time.Time
	lastErr   string
	lastInfo  string
	pending   map[string]any // autorizzazione in corso (device flow)
	verFolder string         // cartella versioni del giro corrente
	httpc     *http.Client
}

const (
	gScope     = "https://www.googleapis.com/auth/drive.file"
	folderMime = "application/vnd.google-apps.folder"
)

// Indirizzi di Google: variabili solo perché i test li puntano a un Drive finto.
var (
	gDriveAPI  = "https://www.googleapis.com/drive/v3"
	gUploadAPI = "https://www.googleapis.com/upload/drive/v3"
	gTokenURL  = "https://oauth2.googleapis.com/token"
)

func newGDrive(h *Hub) *GDrive {
	g := &GDrive{h: h, trigger: make(chan struct{}, 1), httpc: &http.Client{Timeout: 0}}
	loadJSON(g.statePath(), &g.st)
	g.lastOK, g.lastInfo = g.st.LastOK, g.st.LastInfo
	if g.st.Folders == nil {
		g.st.Folders = map[string]string{}
	}
	if g.st.Files == nil {
		g.st.Files = map[string]*gdFile{}
	}
	return g
}

func (g *GDrive) statePath() string { return filepath.Join(g.h.dir, "gdrive-state.json") }
func (g *GDrive) saveState() error  { return saveJSON(g.statePath(), &g.st) }

func (g *GDrive) cfg() GDriveConfig {
	g.h.mu.Lock()
	defer g.h.mu.Unlock()
	c := g.h.cfg.GDrive
	if c.FolderName == "" {
		c.FolderName = "Encluca"
	}
	if c.RetentionDays <= 0 {
		c.RetentionDays = 30
	}
	if c.IntervalMin <= 0 {
		c.IntervalMin = 60
	}
	return c
}

// changed viene chiamata (con h.mu acquisito) a ogni modifica sull'hub.
func (g *GDrive) changed() {
	g.mu.Lock()
	g.dirty = true
	g.lastChg = time.Now()
	g.mu.Unlock()
}

func (g *GDrive) status() map[string]any {
	c := g.cfg()
	g.mu.Lock()
	defer g.mu.Unlock()
	return map[string]any{
		"configured": c.ClientID != "", "authorized": c.RefreshToken != "",
		"folderName": c.FolderName, "retentionDays": c.RetentionDays, "intervalMin": c.IntervalMin,
		"clientId": c.ClientID, "running": g.running, "progress": g.prog.snapshot(), "lastRun": g.lastRun, "lastOK": g.lastOK,
		"lastError": g.lastErr, "lastInfo": g.lastInfo, "pending": g.pending, "dirty": g.dirty,
	}
}

func (g *GDrive) loop(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		manual := false
		select {
		case <-ctx.Done():
			return
		case <-g.trigger:
			manual = true
		case <-t.C:
		}
		c := g.cfg()
		if c.RefreshToken == "" {
			continue
		}
		g.mu.Lock()
		due := manual ||
			(g.dirty && time.Since(g.lastChg) > 60*time.Second) ||
			time.Since(g.lastRun) > time.Duration(c.IntervalMin)*time.Minute
		g.mu.Unlock()
		if !due {
			continue
		}
		g.runBackup(ctx, c)
	}
}

func (g *GDrive) runBackup(ctx context.Context, c GDriveConfig) {
	g.mu.Lock()
	g.running = true
	g.dirty = false
	g.lastRun = time.Now()
	g.verFolder = ""
	g.mu.Unlock()
	up, moved, err := g.backup(ctx, c)
	g.mu.Lock()
	g.running = false
	g.prog = nil
	if err != nil && ctx.Err() != nil {
		g.dirty = true
		g.h.log.Print(T("Backup Google Drive interrotto per l'arresto: riprenderà al riavvio"))
	} else if err != nil {
		g.lastErr = err.Error()
		g.dirty = true // riprova al prossimo giro
		g.h.log.Printf(T("Backup Google Drive fallito: %v"), err)
	} else {
		g.lastErr = ""
		g.lastOK = time.Now()
		g.st.LastOK = g.lastOK
		g.lastInfo = fmt.Sprintf(T("%d caricati, %d spostati nelle versioni"), up, moved)
		g.st.LastInfo = g.lastInfo
		g.saveState()
		if up+moved > 0 {
			g.h.log.Printf(T("Backup Google Drive completato: %s"), g.lastInfo)
		}
	}
	g.mu.Unlock()
}

// ---------- OAuth ----------

func (g *GDrive) token(ctx context.Context, c GDriveConfig) (string, error) {
	g.mu.Lock()
	if g.access != "" && time.Until(g.expiry) > time.Minute {
		t := g.access
		g.mu.Unlock()
		return t, nil
	}
	g.mu.Unlock()
	form := url.Values{"client_id": {c.ClientID}, "client_secret": {c.ClientSecret}, "refresh_token": {c.RefreshToken}, "grant_type": {"refresh_token"}}
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		Error       string `json:"error"`
		Desc        string `json:"error_description"`
	}
	if err := g.postForm(ctx, gTokenURL, form, &out); err != nil && out.Error == "" {
		return "", err
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf(T("token Google non ottenuto: %s %s"), out.Error, out.Desc)
	}
	g.mu.Lock()
	g.access = out.AccessToken
	g.expiry = time.Now().Add(time.Duration(out.ExpiresIn) * time.Second)
	g.mu.Unlock()
	return out.AccessToken, nil
}

func (g *GDrive) postForm(ctx context.Context, u string, form url.Values, out any) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := g.httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	json.Unmarshal(b, out)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// startDeviceAuth avvia l'autorizzazione "da dispositivo": l'utente apre un URL e inserisce un codice.
func (g *GDrive) startDeviceAuth(c GDriveConfig) (map[string]any, error) {
	var dc struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURL string `json:"verification_url"`
		ExpiresIn       int    `json:"expires_in"`
		Interval        int    `json:"interval"`
		Error           string `json:"error"`
		Desc            string `json:"error_description"`
	}
	err := g.postForm(context.Background(), "https://oauth2.googleapis.com/device/code", url.Values{"client_id": {c.ClientID}, "scope": {gScope}}, &dc)
	if err != nil || dc.DeviceCode == "" {
		return nil, fmt.Errorf(T("Google ha rifiutato la richiesta: %v %s %s"), err, dc.Error, dc.Desc)
	}
	p := map[string]any{"userCode": dc.UserCode, "url": dc.VerificationURL, "expires": time.Now().Add(time.Duration(dc.ExpiresIn) * time.Second)}
	g.mu.Lock()
	g.pending = p
	g.mu.Unlock()
	go func() {
		interval := time.Duration(max(dc.Interval, 5)) * time.Second
		deadline := time.Now().Add(time.Duration(dc.ExpiresIn) * time.Second)
		for time.Now().Before(deadline) {
			time.Sleep(interval)
			var tok struct {
				RefreshToken string `json:"refresh_token"`
				AccessToken  string `json:"access_token"`
				ExpiresIn    int    `json:"expires_in"`
				Error        string `json:"error"`
			}
			g.postForm(context.Background(), "https://oauth2.googleapis.com/token", url.Values{
				"client_id": {c.ClientID}, "client_secret": {c.ClientSecret}, "device_code": {dc.DeviceCode},
				"grant_type": {"urn:ietf:params:oauth:grant-type:device_code"},
			}, &tok)
			switch tok.Error {
			case "authorization_pending":
				continue
			case "slow_down":
				interval += 5 * time.Second
				continue
			case "":
			default:
				g.mu.Lock()
				g.pending = nil
				g.lastErr = T("autorizzazione Google non riuscita: ") + tok.Error
				g.mu.Unlock()
				return
			}
			if tok.RefreshToken != "" {
				g.h.mu.Lock()
				g.h.cfg.GDrive.RefreshToken = tok.RefreshToken
				g.h.saveCfg()
				g.h.mu.Unlock()
				g.mu.Lock()
				g.access, g.expiry = tok.AccessToken, time.Now().Add(time.Duration(tok.ExpiresIn)*time.Second)
				g.pending = nil
				g.lastErr = ""
				g.mu.Unlock()
				g.h.log.Print(T("Google Drive autorizzato"))
				g.kick()
				return
			}
		}
		g.mu.Lock()
		g.pending = nil
		g.mu.Unlock()
	}()
	return p, nil
}

func (g *GDrive) kick() {
	select {
	case g.trigger <- struct{}{}:
	default:
	}
}

// ---------- chiamate Drive ----------

type apiError struct {
	Code int
	Body string
}

func (e *apiError) Error() string { return fmt.Sprintf("Drive HTTP %d: %s", e.Code, e.Body) }

func isNotFound(err error) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.Code == http.StatusNotFound
}

func (g *GDrive) do(ctx context.Context, c GDriveConfig, method, u string, body io.Reader, size int64, hdr map[string]string, out any) (*http.Response, error) {
	tok, err := g.token(ctx, c)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	if size >= 0 {
		req.ContentLength = size
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := g.httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusUnauthorized {
		g.mu.Lock()
		g.access = ""
		g.mu.Unlock()
	}
	if resp.StatusCode >= 300 {
		return resp, &apiError{resp.StatusCode, strings.TrimSpace(string(b))}
	}
	if out != nil && len(b) > 0 {
		json.Unmarshal(b, out)
	}
	return resp, nil
}

func (g *GDrive) jsonCall(ctx context.Context, c GDriveConfig, method, u string, in, out any) error {
	var body io.Reader
	var size int64 = -1
	hdr := map[string]string{}
	if in != nil {
		b, _ := json.Marshal(in)
		body, size = bytes.NewReader(b), int64(len(b))
		hdr["Content-Type"] = "application/json; charset=UTF-8"
	}
	_, err := g.do(ctx, c, method, u, body, size, hdr, out)
	return err
}

func (g *GDrive) createFolder(ctx context.Context, c GDriveConfig, name, parent string) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	err := g.jsonCall(ctx, c, http.MethodPost, gDriveAPI+"/files?fields=id", map[string]any{"name": name, "mimeType": folderMime, "parents": []string{parent}}, &out)
	return out.ID, err
}

func (g *GDrive) ensureFolder(ctx context.Context, c GDriveConfig, base, dir string) (string, error) {
	if dir == "." || dir == "" || dir == "/" {
		return base, nil
	}
	key := base + "|" + dir
	if id, ok := g.st.Folders[key]; ok {
		return id, nil
	}
	parent, err := g.ensureFolder(ctx, c, base, path.Dir(dir))
	if err != nil {
		return "", err
	}
	id, err := g.createFolder(ctx, c, path.Base(dir), parent)
	if err != nil {
		return "", err
	}
	g.st.Folders[key] = id
	return id, nil
}

func (g *GDrive) upload(ctx context.Context, c GDriveConfig, local, name, parent string) (string, error) {
	f, err := os.Open(local)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, _ := f.Stat()
	meta, _ := json.Marshal(map[string]any{"name": name, "parents": []string{parent}})
	resp, err := g.do(ctx, c, http.MethodPost, gUploadAPI+"/files?uploadType=resumable&fields=id", bytes.NewReader(meta), int64(len(meta)), map[string]string{
		"Content-Type":            "application/json; charset=UTF-8",
		"X-Upload-Content-Type":   "application/octet-stream",
		"X-Upload-Content-Length": strconv.FormatInt(info.Size(), 10),
	}, nil)
	if err != nil {
		return "", err
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", errors.New(T("Drive non ha restituito l'URL di upload"))
	}
	var out struct {
		ID string `json:"id"`
	}
	if _, err := g.do(ctx, c, http.MethodPut, loc, f, info.Size(), map[string]string{"Content-Type": "application/octet-stream"}, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

func (g *GDrive) updateContent(ctx context.Context, c GDriveConfig, id string, data []byte) error {
	_, err := g.do(ctx, c, http.MethodPatch, gUploadAPI+"/files/"+id+"?uploadType=media", bytes.NewReader(data), int64(len(data)), map[string]string{"Content-Type": "application/octet-stream"}, nil)
	return err
}

func (g *GDrive) uploadBytes(ctx context.Context, c GDriveConfig, data []byte, name, parent string) (string, error) {
	tmp := filepath.Join(g.h.tmpDir(), "gd-"+randHex(6))
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return "", err
	}
	defer os.Remove(tmp)
	return g.upload(ctx, c, tmp, name, parent)
}

// moveToVersions sposta la vecchia copia di un file nella cartella _versioni/<data-ora>/, mantenendo il percorso.
func (g *GDrive) moveToVersions(ctx context.Context, c GDriveConfig, p string, f *gdFile) error {
	if g.verFolder == "" {
		id, err := g.createFolder(ctx, c, time.Now().Format("2006-01-02_150405"), g.st.VersionsID)
		if err != nil {
			return err
		}
		g.verFolder = id
	}
	parent, err := g.ensureFolder(ctx, c, g.verFolder, path.Dir(p))
	if err != nil {
		return err
	}
	u := fmt.Sprintf("%s/files/%s?addParents=%s&removeParents=%s&fields=id", gDriveAPI, f.ID, parent, f.Parent)
	err = g.jsonCall(ctx, c, http.MethodPatch, u, map[string]any{}, nil)
	if isNotFound(err) {
		return nil // già sparito da Drive
	}
	return err
}

const recoveryReadme = `Encluca - backup cifrato
==========================

Questa cartella contiene una copia CIFRATA della tua cassaforte.

- vault/              file cifrati (formato rclone crypt, nomi cifrati)
- _versioni/          copie precedenti di file modificati o cancellati, per data
- encluca-vault.json  parametri pubblici della cassaforte (contiene il "salt")

Recupero senza Encluca, con rclone (https://rclone.org):
  1. configura un remote Google Drive, es. "gd"
  2. configura un remote di tipo "crypt" con:
       remote    = gd:Encluca/vault
       filename_encryption = standard
       directory_name_encryption = true
       password  = la password della cassaforte
       password2 = il valore "salt" in encluca-vault.json
  3. rclone copy <remote-crypt>: /cartella/di/destinazione
`

func (g *GDrive) backup(ctx context.Context, c GDriveConfig) (uploaded, moved int, err error) {
	defer g.saveState()
	// struttura cartelle
	if g.st.RootID != "" {
		if err := g.jsonCall(ctx, c, http.MethodGet, gDriveAPI+"/files/"+g.st.RootID+"?fields=id,trashed", nil, nil); isNotFound(err) {
			g.h.log.Print(T("Cartella di backup non più presente su Drive: la ricreo"))
			g.st = gdState{Folders: map[string]string{}, Files: map[string]*gdFile{}}
		} else if err != nil {
			return 0, 0, err
		}
	}
	if g.st.RootID == "" {
		if g.st.RootID, err = g.createFolder(ctx, c, c.FolderName, "root"); err != nil {
			return
		}
		if g.st.VaultID, err = g.createFolder(ctx, c, "vault", g.st.RootID); err != nil {
			return
		}
		if g.st.VersionsID, err = g.createFolder(ctx, c, "_versioni", g.st.RootID); err != nil {
			return
		}
		g.saveState()
	}
	if g.st.ReadmeID == "" {
		if g.st.ReadmeID, err = g.uploadBytes(ctx, c, []byte(recoveryReadme), "LEGGIMI-RECUPERO.txt", g.st.RootID); err != nil {
			return
		}
	}
	if meta, rerr := os.ReadFile(g.h.metaPath()); rerr == nil && sha256Hex(meta) != g.st.MetaSHA {
		if g.st.MetaID == "" {
			g.st.MetaID, err = g.uploadBytes(ctx, c, meta, "encluca-vault.json", g.st.RootID)
		} else {
			err = g.updateContent(ctx, c, g.st.MetaID, meta)
		}
		if err != nil {
			return
		}
		g.st.MetaSHA = sha256Hex(meta)
	}

	// se il giro precedente non è terminato regolarmente, prima di caricare si controlla
	// su Drive (vedi gdState.Clean); poi si segna il giro come in corso
	recovering := !g.st.Clean && len(g.st.Files) > 0
	g.st.Clean = false
	g.saveState()

	// istantanea dell'indice
	type liveFile struct {
		sha  string
		size int64
	}
	g.h.mu.Lock()
	live := map[string]liveFile{}
	liveDirs := map[string]bool{}
	for p, e := range g.h.idx.Entries {
		switch {
		case e.Del:
		case e.Dir:
			liveDirs[p] = true
		default:
			live[p] = liveFile{e.SHA, e.Size}
		}
	}
	g.h.mu.Unlock()

	// cartelle vuote: solo la cartella, nessun contenuto
	if g.st.Dirs == nil {
		g.st.Dirs = map[string]bool{}
	}
	for p := range liveDirs {
		if g.st.Dirs[p] {
			continue
		}
		if _, err = g.ensureFolder(ctx, c, g.st.VaultID, p); err != nil {
			return
		}
		g.st.Dirs[p] = true
	}
	for p := range g.st.Dirs {
		if liveDirs[p] {
			continue
		}
		key := g.st.VaultID + "|" + p
		if id, ok := g.st.Folders[key]; ok {
			var trashed bool
			if trashed, err = g.trashFolderIfEmpty(ctx, c, id); err != nil {
				return
			}
			if trashed { // una cartella con contenuto resta, e resta nota, per non duplicarla
				delete(g.st.Folders, key)
			}
		}
		delete(g.st.Dirs, p)
	}

	// elenco del lavoro da fare, per mostrare l'avanzamento
	var todo []string
	prog := &progress{}
	for p, lf := range live {
		if f := g.st.Files[p]; f == nil || f.SHA != lf.sha {
			todo = append(todo, p)
			prog.TotalBytes += lf.size
		}
	}
	for p := range g.st.Files {
		if _, ok := live[p]; !ok {
			prog.Total++
		}
	}
	sort.Strings(todo)
	prog.Total += len(todo)
	g.mu.Lock()
	g.prog = prog
	g.mu.Unlock()
	advance := func(size int64) {
		g.mu.Lock()
		prog.Done++
		prog.DoneBytes += size
		g.mu.Unlock()
	}

	n := 0
	for _, p := range todo {
		if ctx.Err() != nil {
			return uploaded, moved, ctx.Err()
		}
		sha, size := live[p].sha, live[p].size
		f := g.st.Files[p]
		if f != nil {
			if err = g.moveToVersions(ctx, c, p, f); err != nil {
				return
			}
			moved++
			delete(g.st.Files, p)
		}
		var parent, id string
		if parent, err = g.ensureFolder(ctx, c, g.st.VaultID, path.Dir(p)); err != nil {
			return
		}
		if recovering {
			// file caricati nel giro interrotto ma non registrati: si spostano nelle
			// versioni invece di lasciare un doppione accanto alla copia nuova
			var orphans []string
			if orphans, err = g.findByName(ctx, c, path.Base(p), parent); err != nil {
				return
			}
			for _, id := range orphans {
				if err = g.moveToVersions(ctx, c, p, &gdFile{ID: id, Parent: parent}); err != nil {
					return
				}
			}
		}
		local := filepath.Join(g.h.vaultDir(), filepath.FromSlash(p))
		if id, err = g.upload(ctx, c, local, path.Base(p), parent); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				err = nil // cancellato nel frattempo
				advance(size)
				continue
			}
			return
		}
		g.st.Files[p] = &gdFile{ID: id, SHA: sha, Parent: parent}
		uploaded++
		advance(size)
		if n++; n%20 == 0 {
			g.saveState()
		}
	}
	for p, f := range g.st.Files {
		if _, ok := live[p]; ok {
			continue
		}
		if err = g.moveToVersions(ctx, c, p, f); err != nil {
			return
		}
		delete(g.st.Files, p)
		moved++
		advance(0)
	}
	if err = g.pruneVersions(ctx, c); err == nil {
		g.st.Clean = true
	}
	return
}

// trashFolderIfEmpty mette nel cestino di Drive la cartella id, se non contiene nulla.
// Restituisce true se la cartella non c'è più.
func (g *GDrive) trashFolderIfEmpty(ctx context.Context, c GDriveConfig, id string) (bool, error) {
	q := url.QueryEscape(fmt.Sprintf("'%s' in parents and trashed = false", strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(id)))
	var out struct {
		Files []struct{ ID string } `json:"files"`
	}
	if err := g.jsonCall(ctx, c, http.MethodGet, gDriveAPI+"/files?pageSize=1&fields=files(id)&q="+q, nil, &out); err != nil {
		return false, err
	}
	if len(out.Files) > 0 {
		return false, nil
	}
	err := g.jsonCall(ctx, c, http.MethodPatch, gDriveAPI+"/files/"+id, map[string]any{"trashed": true}, nil)
	if isNotFound(err) {
		return true, nil
	}
	return err == nil, err
}

// findByName elenca i file (non cartelle) chiamati name nella cartella parent.
func (g *GDrive) findByName(ctx context.Context, c GDriveConfig, name, parent string) ([]string, error) {
	esc := strings.NewReplacer(`\`, `\\`, `'`, `\'`)
	q := url.QueryEscape(fmt.Sprintf("'%s' in parents and name = '%s' and trashed = false and mimeType != '%s'", esc.Replace(parent), esc.Replace(name), folderMime))
	var out struct {
		Files []struct {
			ID string `json:"id"`
		} `json:"files"`
	}
	if err := g.jsonCall(ctx, c, http.MethodGet, gDriveAPI+"/files?pageSize=100&fields=files(id)&q="+q, nil, &out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Files))
	for _, f := range out.Files {
		ids = append(ids, f.ID)
	}
	return ids, nil
}

func (g *GDrive) pruneVersions(ctx context.Context, c GDriveConfig) error {
	q := url.QueryEscape(fmt.Sprintf("'%s' in parents and trashed = false and mimeType = '%s'", g.st.VersionsID, folderMime))
	var out struct {
		Files []struct{ ID, Name string } `json:"files"`
	}
	if err := g.jsonCall(ctx, c, http.MethodGet, gDriveAPI+"/files?pageSize=1000&fields=files(id,name)&q="+q, nil, &out); err != nil {
		return err
	}
	limit := time.Now().AddDate(0, 0, -c.RetentionDays)
	for _, f := range out.Files {
		t, err := time.ParseInLocation("2006-01-02_150405", f.Name, time.Local)
		if err != nil || t.After(limit) {
			continue
		}
		if err := g.jsonCall(ctx, c, http.MethodDelete, gDriveAPI+"/files/"+f.ID, nil, nil); err != nil && !isNotFound(err) {
			return err
		}
		for k := range g.st.Folders {
			if strings.HasPrefix(k, f.ID+"|") {
				delete(g.st.Folders, k)
			}
		}
		g.h.log.Printf(T("Versioni del %s eliminate (più vecchie di %d giorni)"), f.Name, c.RetentionDays)
	}
	return nil
}

// ---------- API amministrazione ----------

func (g *GDrive) apiConfig(w http.ResponseWriter, r *http.Request) {
	var body GDriveConfig
	if err := readBody(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, T("dati non validi"))
		return
	}
	g.h.mu.Lock()
	cur := &g.h.cfg.GDrive
	if strings.TrimSpace(body.ClientID) != "" && body.ClientID != cur.ClientID {
		cur.ClientID = strings.TrimSpace(body.ClientID)
		cur.RefreshToken = ""
	}
	if strings.TrimSpace(body.ClientSecret) != "" {
		cur.ClientSecret = strings.TrimSpace(body.ClientSecret)
	}
	if body.FolderName != "" {
		cur.FolderName = body.FolderName
	}
	if body.RetentionDays > 0 {
		cur.RetentionDays = body.RetentionDays
	}
	if body.IntervalMin > 0 {
		cur.IntervalMin = body.IntervalMin
	}
	g.h.saveCfg()
	g.h.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (g *GDrive) apiAuth(w http.ResponseWriter, r *http.Request) {
	c := g.cfg()
	if c.ClientID == "" || c.ClientSecret == "" {
		writeErr(w, http.StatusBadRequest, T("inserisci prima Client ID e Client Secret"))
		return
	}
	p, err := g.startDeviceAuth(c)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (g *GDrive) apiRun(w http.ResponseWriter, r *http.Request) {
	if g.cfg().RefreshToken == "" {
		writeErr(w, http.StatusBadRequest, T("Google Drive non è ancora autorizzato"))
		return
	}
	g.kick()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (g *GDrive) apiDisconnect(w http.ResponseWriter, r *http.Request) {
	g.h.mu.Lock()
	g.h.cfg.GDrive.RefreshToken = ""
	g.h.saveCfg()
	g.h.mu.Unlock()
	g.mu.Lock()
	g.access = ""
	g.mu.Unlock()
	g.h.log.Print(T("Google Drive scollegato"))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
