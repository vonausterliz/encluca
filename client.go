package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"encluca/internal/rcrypt"
)

const (
	readmeName   = "LEGGIMI - CASSAFORTE APERTA.txt"
	tmpPrefix    = ".encluca-tmp-"
	checkPlain   = "encluca-password-ok"
	scanInterval = 2 * time.Second
)

const readmeText = `QUESTA CARTELLA CONTIENE I TUOI FILE DECRIPTATI (IN CHIARO).

È stata creata da Encluca quando hai aperto la cassaforte.
Ogni modifica che fai qui viene cifrata e inviata al NAS entro pochi secondi
(oppure appena il NAS torna raggiungibile).

Quando hai finito, chiudi la cassaforte da Encluca (http://127.0.0.1:8484):
la cartella verrà cancellata e sul disco resteranno solo dati cifrati.

Questo file non viene sincronizzato.
`

type vaultMeta struct {
	V       int    `json:"v"`
	Salt    string `json:"salt"`
	Check   string `json:"check"`
	Created string `json:"created"`
	Format  string `json:"format"`
}

type clientConfig struct {
	HubURL      string `json:"hubUrl"`
	Token       string `json:"token"`
	Fingerprint string `json:"fingerprint"`
	DeviceName  string `json:"deviceName"`
	WorkDir     string `json:"workDir"`
}

type plainSnap struct {
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"`
	Hash  string `json:"hash"`
}

type fstate struct {
	Rev      int64      `json:"rev"`
	SHA      string     `json:"sha,omitempty"`
	Size     int64      `json:"size,omitempty"`
	MTime    int64      `json:"mtime,omitempty"`
	Del      bool       `json:"del,omitempty"`
	Pending  string     `json:"pending,omitempty"`  // "" | "put" | "del"
	Conflict int64      `json:"conflict,omitempty"` // revisione remota in conflitto, da risolvere a cassaforte aperta
	Plain    *plainSnap `json:"plain,omitempty"`
}

type clientState struct {
	Seq   int64              `json:"seq"`
	Open  bool               `json:"open"`
	Files map[string]*fstate `json:"files"`
}

type Client struct {
	dir  string
	log  *ringLog
	mu   sync.Mutex
	cfg  clientConfig
	st   clientState
	cip  *rcrypt.Cipher
	hc   *http.Client
	wake chan struct{}

	online    bool
	revoked   bool
	lastErr   string
	lastSync  time.Time
	conflicts []string
	problems  map[string]string
	encCache  map[string]string
	massDel   int // eliminazioni bloccate in attesa di conferma
	allowDel  bool
	busy      string
	pullReset chan struct{}
}

func (c *Client) cfgPath() string   { return filepath.Join(c.dir, "config.json") }
func (c *Client) statePath() string { return filepath.Join(c.dir, "state.json") }
func (c *Client) metaCache() string { return filepath.Join(c.dir, "vault-meta.json") }
func (c *Client) mirrorDir() string { return filepath.Join(c.dir, "mirror") }
func (c *Client) tmpDir() string    { return filepath.Join(c.dir, "tmp") }
func (c *Client) mirrorPath(enc string) string {
	return filepath.Join(c.mirrorDir(), filepath.FromSlash(enc))
}

func newClient(dir string) (*Client, error) {
	c := &Client{dir: dir, wake: make(chan struct{}, 1), pullReset: make(chan struct{}, 1), problems: map[string]string{}, encCache: map[string]string{}}
	for _, d := range []string{dir, c.mirrorDir(), c.tmpDir()} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	c.log = newRingLog(filepath.Join(dir, "encluca.log"), 300)
	if ents, err := os.ReadDir(c.tmpDir()); err == nil {
		for _, e := range ents {
			os.Remove(filepath.Join(c.tmpDir(), e.Name()))
		}
	}
	if err := loadJSON(c.cfgPath(), &c.cfg); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if c.cfg.WorkDir == "" {
		c.cfg.WorkDir = defaultWorkDir()
	}
	if c.cfg.DeviceName == "" {
		c.cfg.DeviceName, _ = os.Hostname()
	}
	if err := loadJSON(c.statePath(), &c.st); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if c.st.Files == nil {
		c.st.Files = map[string]*fstate{}
	}
	c.setupHTTP()
	return c, nil
}

func (c *Client) setupHTTP() {
	fp := c.cfg.Fingerprint
	c.hc = &http.Client{
		Timeout: 0,
		Transport: &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			TLSHandshakeTimeout: 15 * time.Second,
			IdleConnTimeout:     90 * time.Second,
			TLSClientConfig: &tls.Config{
				// Il certificato dell'hub è autofirmato: lo verifichiamo tramite impronta (pinning),
				// ricevuta nel codice di abbinamento. Nessun altro server viene accettato.
				InsecureSkipVerify: true,
				VerifyConnection: func(cs tls.ConnectionState) error {
					if len(cs.PeerCertificates) == 0 {
						return errors.New("nessun certificato dal NAS")
					}
					sum := sha256.Sum256(cs.PeerCertificates[0].Raw)
					if hex.EncodeToString(sum[:]) != fp {
						return errors.New("il certificato del NAS non corrisponde a quello abbinato")
					}
					return nil
				},
			},
		},
	}
}

func (c *Client) saveState() {
	if err := saveJSON(c.statePath(), &c.st); err != nil {
		c.log.Printf("ERRORE salvataggio stato: %v", err)
	}
}

func (c *Client) paired() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cfg.Token != "" && !c.revoked
}

func (c *Client) kick() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// ---------- HTTP verso l'hub ----------

type httpStatusErr struct {
	Code int
	Body []byte
}

func (e *httpStatusErr) Error() string {
	return fmt.Sprintf("NAS HTTP %d: %s", e.Code, strings.TrimSpace(string(e.Body)))
}

func (c *Client) req(ctx context.Context, method, p string, q url.Values, body io.Reader, size int64) (*http.Response, error) {
	c.mu.Lock()
	base, tok := c.cfg.HubURL, c.cfg.Token
	c.mu.Unlock()
	u := strings.TrimRight(base, "/") + p
	if q != nil {
		u += "?" + q.Encode()
	}
	r, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	if size >= 0 && body != nil {
		r.ContentLength = size
	}
	r.Header.Set("Authorization", "Bearer "+tok)
	resp, err := c.hc.Do(r)
	if err != nil {
		c.setOnline(false, "NAS non raggiungibile")
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		c.mu.Lock()
		c.revoked = true
		c.lastErr = "questo dispositivo è stato revocato dal NAS: abbinalo di nuovo"
		c.mu.Unlock()
		return nil, errors.New("dispositivo revocato")
	}
	c.setOnline(true, "")
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		resp.Body.Close()
		return nil, &httpStatusErr{resp.StatusCode, b}
	}
	return resp, nil
}

func (c *Client) setOnline(on bool, msg string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if on != c.online {
		if on {
			c.log.Printf("NAS raggiungibile")
		} else {
			c.log.Printf("NAS non raggiungibile: le modifiche restano in coda")
		}
	}
	c.online = on
	if on {
		c.lastErr = ""
	} else if msg != "" {
		c.lastErr = msg
	}
}

func (c *Client) getJSON(ctx context.Context, p string, q url.Values, out any) error {
	resp, err := c.req(ctx, http.MethodGet, p, q, nil, -1)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) fetchMeta(ctx context.Context) (*vaultMeta, error) {
	var m vaultMeta
	err := c.getJSON(ctx, "/api/v1/meta", nil, &m)
	if err == nil {
		b, _ := json.Marshal(&m)
		writeFileAtomic(c.metaCache(), b, 0o600)
		return &m, nil
	}
	var se *httpStatusErr
	if errors.As(err, &se) && se.Code == http.StatusNotFound {
		return nil, nil
	}
	// offline: usa la copia locale
	if lerr := loadJSON(c.metaCache(), &m); lerr == nil {
		return &m, nil
	}
	return nil, err
}

// ---------- cifratura nomi ----------

func (c *Client) encPathLocked(rel string) (string, error) {
	if e, ok := c.encCache[rel]; ok {
		return e, nil
	}
	e, err := c.cip.EncryptPath(rel)
	if err != nil {
		return "", err
	}
	if len(c.encCache) > 200000 {
		c.encCache = map[string]string{}
	}
	c.encCache[rel] = e
	return e, nil
}

func ignored(name string) bool {
	if name == readmeName || strings.HasPrefix(name, tmpPrefix) {
		return true
	}
	switch name {
	case ".DS_Store", "Thumbs.db", "desktop.ini", ".directory":
		return true
	}
	return strings.HasPrefix(name, "~$") || strings.HasPrefix(name, ".~lock.") ||
		strings.HasSuffix(name, ".crdownload") || strings.HasSuffix(name, ".part") ||
		strings.HasPrefix(name, "._")
}

// ---------- scansione della cartella aperta ----------

// encryptToMirror cifra un file in chiaro nella copia locale cifrata.
// Restituisce sha e dimensione del cifrato e l'hash del chiaro.
func (c *Client) encryptToMirror(src, enc string) (csha string, csize int64, phash string, err error) {
	f, err := os.Open(src)
	if err != nil {
		return
	}
	defer f.Close()
	tmp, err := os.CreateTemp(c.tmpDir(), "enc-*")
	if err != nil {
		return
	}
	defer os.Remove(tmp.Name())
	ph := sha256.New()
	ch := sha256.New()
	cw := &countWriter{w: io.MultiWriter(tmp, ch)}
	err = c.cip.EncryptStream(cw, io.TeeReader(f, ph))
	if err == nil {
		err = tmp.Sync()
	}
	tmp.Close()
	if err != nil {
		return
	}
	dst := c.mirrorPath(enc)
	if err = os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return
	}
	if err = os.Rename(tmp.Name(), dst); err != nil {
		return
	}
	return hex.EncodeToString(ch.Sum(nil)), cw.n, hex.EncodeToString(ph.Sum(nil)), nil
}

type countWriter struct {
	w io.Writer
	n int64
}

func (cw *countWriter) Write(p []byte) (int, error) {
	n, err := cw.w.Write(p)
	cw.n += int64(n)
	return n, err
}

// scanFileLocked controlla un file della cartella aperta; se è cambiato lo cifra e lo mette in coda.
func (c *Client) scanFileLocked(enc, rel string, info fs.FileInfo) (bool, error) {
	st := c.st.Files[enc]
	mt, sz := info.ModTime().UnixNano(), info.Size()
	if st != nil && !st.Del && st.Plain != nil && st.Plain.Size == sz && st.Plain.MTime == mt {
		return false, nil
	}
	full := filepath.Join(c.cfg.WorkDir, filepath.FromSlash(rel))
	if st != nil && !st.Del && st.Plain != nil && st.Plain.Size == sz {
		if h, _, err := hashFile(full); err == nil && h == st.Plain.Hash {
			st.Plain.MTime = mt
			return false, nil
		}
	}
	csha, csize, phash, err := c.encryptToMirror(full, enc)
	if err != nil {
		return false, err
	}
	// se il file è cambiato mentre lo leggevamo, riproviamo al giro successivo
	if info2, err := os.Stat(full); err != nil || info2.Size() != sz || info2.ModTime().UnixNano() != mt {
		if st != nil && st.Plain != nil {
			st.Plain.MTime = -1
		}
		return false, nil
	}
	if st == nil {
		st = &fstate{}
		c.st.Files[enc] = st
	}
	st.Del = false
	st.SHA, st.Size, st.MTime = csha, csize, mt
	st.Pending = "put"
	st.Plain = &plainSnap{Size: sz, MTime: mt, Hash: phash}
	c.log.Printf("Modificato: %s", rel)
	return true, nil
}

// scanLocked percorre tutta la cartella aperta. strict=true restituisce errore se un file non è leggibile.
func (c *Client) scanLocked(strict bool) (changed bool, err error) {
	if c.cip == nil {
		return false, nil
	}
	work := c.cfg.WorkDir
	if _, err := os.Stat(work); err != nil {
		c.lastErr = "la cartella aperta non esiste più: " + work
		return false, errors.New(c.lastErr)
	}
	seen := map[string]bool{}
	var firstErr error
	walkErr := filepath.WalkDir(work, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if p == work {
			return nil
		}
		if ignored(d.Name()) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		rel, _ := filepath.Rel(work, p)
		rel = filepath.ToSlash(rel)
		enc, err := c.encPathLocked(rel)
		if err != nil {
			c.problems[rel] = "nome troppo lungo per essere cifrato: accorcialo"
			return nil
		}
		delete(c.problems, rel)
		seen[enc] = true
		info, err := d.Info()
		if err != nil {
			return nil
		}
		ch, err := c.scanFileLocked(enc, rel, info)
		if err != nil {
			c.problems[rel] = "non leggibile: " + err.Error()
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", rel, err)
			}
			seen[enc] = true
			return nil
		}
		changed = changed || ch
		return nil
	})
	if walkErr != nil && firstErr == nil {
		firstErr = walkErr
	}
	// file spariti dalla cartella = cancellati dall'utente
	var gone []string
	live := 0
	for enc, st := range c.st.Files {
		if st.Del || st.Plain == nil {
			continue
		}
		live++
		if !seen[enc] {
			gone = append(gone, enc)
		}
	}
	if len(gone) > 20 && len(gone)*2 > live && !c.allowDel {
		// protezione: molte cancellazioni insieme possono essere un errore (o un ransomware)
		if c.massDel != len(gone) {
			c.log.Printf("ATTENZIONE: %d file spariti insieme dalla cartella aperta. Eliminazione sospesa: confermala dall'interfaccia.", len(gone))
		}
		c.massDel = len(gone)
	} else {
		c.massDel = 0
		c.allowDel = false
		for _, enc := range gone {
			st := c.st.Files[enc]
			os.Remove(c.mirrorPath(enc))
			st.Plain = nil
			if st.Rev == 0 {
				delete(c.st.Files, enc)
			} else {
				st.Del, st.SHA, st.Size = true, "", 0
				st.Pending = "del"
			}
			if rel, err := c.cip.DecryptPath(enc); err == nil {
				c.log.Printf("Eliminato: %s", rel)
			}
			changed = true
		}
	}
	if strict && firstErr != nil {
		return changed, firstErr
	}
	return changed, nil
}

// ---------- materializzazione (decifratura nella cartella aperta) ----------

func (c *Client) decryptToWorkLocked(enc string, st *fstate) error {
	rel, err := c.cip.DecryptPath(enc)
	if err != nil {
		return fmt.Errorf("nome non decifrabile: %w", err)
	}
	src, err := os.Open(c.mirrorPath(enc))
	if err != nil {
		return err
	}
	defer src.Close()
	dst := filepath.Join(c.cfg.WorkDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), tmpPrefix+"*")
	if err != nil {
		return err
	}
	ph := sha256.New()
	err = c.cip.DecryptStream(io.MultiWriter(tmp, ph), src)
	tmp.Close()
	if err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if st.MTime > 0 {
		t := time.Unix(0, st.MTime)
		os.Chtimes(tmp.Name(), t, t)
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	info, err := os.Stat(dst)
	if err != nil {
		return err
	}
	st.Plain = &plainSnap{Size: info.Size(), MTime: info.ModTime().UnixNano(), Hash: hex.EncodeToString(ph.Sum(nil))}
	c.encCache[rel] = enc
	return nil
}

// ---------- applicazione delle modifiche remote ----------

func conflictName(rel, device string) string {
	dir, base := path.Split(rel)
	ext := path.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	if stem == "" {
		stem, ext = base, ""
	}
	device = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, r) {
			return '_'
		}
		return r
	}, device)
	return fmt.Sprintf("%s%s (conflitto %s %s)%s", dir, stem, device, time.Now().Format("2006-01-02 15.04.05"), ext)
}

// resolveConflictLocked sposta la versione locale in un file "(conflitto …)" che verrà caricato a parte.
func (c *Client) resolveConflictLocked(enc string) error {
	st := c.st.Files[enc]
	rel, err := c.cip.DecryptPath(enc)
	if err != nil {
		return err
	}
	newRel := conflictName(rel, c.cfg.DeviceName)
	newEnc, err := c.encPathLocked(newRel)
	if err != nil {
		newRel = conflictName(path.Join(path.Dir(rel), "file"), c.cfg.DeviceName)
		if newEnc, err = c.encPathLocked(newRel); err != nil {
			return err
		}
	}
	// il contenuto cifrato non dipende dal nome: basta spostare il file
	dst := c.mirrorPath(newEnc)
	os.MkdirAll(filepath.Dir(dst), 0o700)
	if err := os.Rename(c.mirrorPath(enc), dst); err != nil {
		return err
	}
	if c.st.Open {
		os.MkdirAll(filepath.Dir(filepath.Join(c.cfg.WorkDir, filepath.FromSlash(newRel))), 0o700)
		os.Rename(filepath.Join(c.cfg.WorkDir, filepath.FromSlash(rel)), filepath.Join(c.cfg.WorkDir, filepath.FromSlash(newRel)))
	}
	c.st.Files[newEnc] = &fstate{SHA: st.SHA, Size: st.Size, MTime: st.MTime, Pending: "put", Plain: st.Plain}
	st.Pending, st.Conflict, st.Plain = "", 0, nil
	st.SHA, st.Size = "", 0
	c.conflicts = append([]string{newRel}, c.conflicts...)
	if len(c.conflicts) > 50 {
		c.conflicts = c.conflicts[:50]
	}
	c.log.Printf("CONFLITTO: %s modificato anche altrove. La tua versione è salvata come %q", rel, path.Base(newRel))
	return nil
}

// syncEntry allinea un file allo stato remoto e.
func (c *Client) syncEntry(ctx context.Context, e *Entry) error {
	c.mu.Lock()
	if c.cip != nil && c.st.Open {
		// prima di decidere, cattura eventuali modifiche locali non ancora viste
		if rel, err := c.cip.DecryptPath(e.Path); err == nil {
			if info, err := os.Stat(filepath.Join(c.cfg.WorkDir, filepath.FromSlash(rel))); err == nil && info.Mode().IsRegular() {
				c.scanFileLocked(e.Path, rel, info)
			}
		}
	}
	st := c.st.Files[e.Path]
	if st != nil && st.Rev >= e.Rev {
		c.mu.Unlock()
		return nil
	}
	if st != nil && st.Pending == "put" {
		if e.Del {
			st.Rev = e.Rev // la nostra modifica "vince" e ricrea il file
			c.mu.Unlock()
			return nil
		}
		if c.cip == nil {
			st.Conflict = e.Rev // da risolvere quando la cassaforte viene aperta
			c.mu.Unlock()
			return nil
		}
		if err := c.resolveConflictLocked(e.Path); err != nil {
			c.mu.Unlock()
			return err
		}
		st = c.st.Files[e.Path]
	}
	if st != nil && st.Pending == "del" {
		st.Pending = "" // la modifica altrui prevale sulla nostra cancellazione
	}
	if e.Del {
		os.Remove(c.mirrorPath(e.Path))
		if c.st.Open && c.cip != nil {
			if rel, err := c.cip.DecryptPath(e.Path); err == nil {
				os.Remove(filepath.Join(c.cfg.WorkDir, filepath.FromSlash(rel)))
				c.log.Printf("Eliminato da un altro dispositivo: %s", rel)
			}
		}
		c.st.Files[e.Path] = &fstate{Rev: e.Rev, Del: true}
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	// scarica fuori dal lock
	resp, err := c.req(ctx, http.MethodGet, "/api/v1/file", url.Values{"p": {e.Path}}, nil, -1)
	if err != nil {
		var se *httpStatusErr
		if errors.As(err, &se) && se.Code == http.StatusNotFound {
			return nil // nel frattempo cancellato: arriverà come modifica successiva
		}
		return err
	}
	defer resp.Body.Close()
	tmp, err := os.CreateTemp(c.tmpDir(), "dl-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	hs := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, hs), resp.Body)
	if err == nil {
		err = tmp.Sync()
	}
	tmp.Close()
	if err != nil {
		return err
	}
	rev, _ := strconv.ParseInt(resp.Header.Get("X-Rev"), 10, 64)
	sum := hex.EncodeToString(hs.Sum(nil))
	if sum != resp.Header.Get("X-Sha") {
		return errors.New("download corrotto, riprovo")
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	st = c.st.Files[e.Path]
	if st != nil && st.Pending == "put" {
		st.Conflict = rev // modificato localmente mentre scaricavamo
		return nil
	}
	dst := c.mirrorPath(e.Path)
	os.MkdirAll(filepath.Dir(dst), 0o700)
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return err
	}
	nst := &fstate{Rev: rev, SHA: sum, Size: n, MTime: e.MTime}
	c.st.Files[e.Path] = nst
	if c.st.Open && c.cip != nil {
		if err := c.decryptToWorkLocked(e.Path, nst); err != nil {
			c.log.Printf("ERRORE decifratura %s: %v", e.Path, err)
		} else if rel, err := c.cip.DecryptPath(e.Path); err == nil {
			c.log.Printf("Aggiornato da un altro dispositivo: %s", rel)
		}
	}
	return nil
}

func (c *Client) pullLoop(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		if !c.paired() {
			select {
			case <-ctx.Done():
				return
			case <-c.pullReset:
			case <-time.After(2 * time.Second):
			}
			continue
		}
		c.mu.Lock()
		since := c.st.Seq
		c.mu.Unlock()
		var res struct {
			Seq     int64    `json:"seq"`
			Entries []*Entry `json:"entries"`
		}
		rctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		err := c.getJSON(rctx, "/api/v1/changes", url.Values{"since": {strconv.FormatInt(since, 10)}, "wait": {"25"}}, &res)
		cancel()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-c.pullReset:
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		backoff = time.Second
		ok := true
		for _, e := range res.Entries {
			if err := c.syncEntry(ctx, e); err != nil {
				c.log.Printf("Sincronizzazione interrotta: %v", err)
				ok = false
				break
			}
			c.mu.Lock()
			c.st.Seq = e.Seq
			c.mu.Unlock()
		}
		c.mu.Lock()
		if ok {
			c.st.Seq = max(c.st.Seq, res.Seq)
			c.lastSync = time.Now()
		}
		if len(res.Entries) > 0 {
			c.saveState()
		}
		c.mu.Unlock()
		if len(res.Entries) > 0 {
			c.kick()
		}
		if !ok {
			time.Sleep(backoff)
		}
	}
}

// ---------- invio delle modifiche locali ----------

func (c *Client) pushOnce(ctx context.Context) {
	c.mu.Lock()
	type job struct {
		enc string
		st  fstate
	}
	var jobs []job
	var deferred []string
	for enc, st := range c.st.Files {
		if st.Conflict > 0 {
			deferred = append(deferred, enc)
			continue
		}
		if st.Pending != "" {
			jobs = append(jobs, job{enc, *st})
		}
	}
	canResolve := c.cip != nil
	c.mu.Unlock()
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].enc < jobs[j].enc })

	// conflitti in sospeso: si risolvono solo con la cassaforte aperta (servono i nomi in chiaro)
	if canResolve {
		for _, enc := range deferred {
			var e Entry
			if err := c.getJSON(ctx, "/api/v1/entry", url.Values{"p": {enc}}, &e); err != nil {
				var se *httpStatusErr
				if errors.As(err, &se) && se.Code == http.StatusNotFound {
					c.mu.Lock()
					if st := c.st.Files[enc]; st != nil {
						st.Conflict, st.Rev = 0, 0
					}
					c.mu.Unlock()
					continue
				}
				return
			}
			c.mu.Lock()
			if st := c.st.Files[enc]; st != nil {
				st.Conflict = 0
				if st.Rev >= e.Rev {
					st.Rev = e.Rev - 1 // forza la rivalutazione
				}
			}
			c.mu.Unlock()
			if err := c.syncEntry(ctx, &e); err != nil {
				c.mu.Lock()
				if st := c.st.Files[enc]; st != nil && st.Pending == "put" {
					st.Conflict = e.Rev
				}
				c.mu.Unlock()
				return
			}
		}
	}

	for _, j := range jobs {
		if ctx.Err() != nil {
			return
		}
		var resp *http.Response
		var err error
		switch j.st.Pending {
		case "put":
			f, ferr := os.Open(c.mirrorPath(j.enc))
			if ferr != nil {
				continue // sostituito nel frattempo: riprova al prossimo giro
			}
			q := url.Values{"p": {j.enc}, "base": {strconv.FormatInt(j.st.Rev, 10)}, "sha": {j.st.SHA}, "mtime": {strconv.FormatInt(j.st.MTime, 10)}}
			resp, err = c.req(ctx, http.MethodPut, "/api/v1/file", q, f, j.st.Size)
			f.Close()
		case "del":
			resp, err = c.req(ctx, http.MethodDelete, "/api/v1/file", url.Values{"p": {j.enc}, "base": {strconv.FormatInt(j.st.Rev, 10)}}, nil, -1)
		}
		var se *httpStatusErr
		if errors.As(err, &se) {
			var cur Entry
			json.Unmarshal(se.Body, &cur)
			c.mu.Lock()
			st := c.st.Files[j.enc]
			switch {
			case st == nil:
			case se.Code == http.StatusConflict && j.st.Pending == "put":
				st.Conflict = max(cur.Rev, st.Rev+1)
			case se.Code == http.StatusConflict && j.st.Pending == "del":
				st.Pending = "" // modificato altrove: lo riscarichiamo
				st.Rev = 0
				c.st.Seq = 0
			case se.Code == http.StatusNotFound && j.st.Pending == "del":
				delete(c.st.Files, j.enc)
			default:
				c.log.Printf("Errore dal NAS: %v", err)
			}
			c.saveState()
			c.mu.Unlock()
			continue
		}
		if err != nil {
			return // offline: riprova più tardi
		}
		var e Entry
		json.NewDecoder(resp.Body).Decode(&e)
		resp.Body.Close()
		c.mu.Lock()
		if st := c.st.Files[j.enc]; st != nil {
			st.Rev = max(st.Rev, e.Rev)
			if st.Pending == j.st.Pending && st.SHA == j.st.SHA {
				st.Pending = ""
			}
		}
		c.lastSync = time.Now()
		c.saveState()
		c.mu.Unlock()
	}
}

func (c *Client) pushLoop(ctx context.Context) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.wake:
		case <-t.C:
		}
		if c.paired() {
			c.pushOnce(ctx)
		}
	}
}

func (c *Client) scanLoop(ctx context.Context) {
	t := time.NewTicker(scanInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		c.mu.Lock()
		var changed bool
		if c.cip != nil && c.st.Open && c.busy == "" {
			changed, _ = c.scanLocked(false)
			if changed {
				c.saveState()
			}
		}
		c.mu.Unlock()
		if changed {
			c.kick()
		}
	}
}

// ---------- operazioni dell'utente ----------

func parsePairCode(code string) (u, tok, fp string, err error) {
	code = strings.TrimSpace(code)
	if !strings.HasPrefix(code, "EL1-") {
		return "", "", "", errors.New("codice di abbinamento non valido")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(code, "EL1-"))
	if err != nil {
		return "", "", "", errors.New("codice di abbinamento non valido")
	}
	var m map[string]string
	if json.Unmarshal(raw, &m) != nil || m["t"] == "" || m["f"] == "" {
		return "", "", "", errors.New("codice di abbinamento non valido")
	}
	return m["u"], m["t"], m["f"], nil
}

func (c *Client) Pair(code, hubURL, name string) error {
	u, tok, fp, err := parsePairCode(code)
	if err != nil {
		return err
	}
	if strings.TrimSpace(hubURL) != "" {
		u = strings.TrimSpace(hubURL)
	}
	if !strings.HasPrefix(u, "https://") {
		u = "https://" + u
	}
	c.mu.Lock()
	if c.st.Open {
		c.mu.Unlock()
		return errors.New("chiudi la cassaforte prima di cambiare NAS")
	}
	old := c.cfg
	c.cfg.HubURL, c.cfg.Token, c.cfg.Fingerprint = strings.TrimRight(u, "/"), tok, fp
	if strings.TrimSpace(name) != "" {
		c.cfg.DeviceName = strings.TrimSpace(name)
	}
	c.setupHTTP()
	c.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := c.fetchMeta(ctx); err != nil {
		c.mu.Lock()
		c.cfg = old
		c.setupHTTP()
		c.mu.Unlock()
		return fmt.Errorf("impossibile contattare il NAS: %w", err)
	}
	c.mu.Lock()
	if old.Fingerprint != fp {
		// nuovo NAS: si riparte da zero con la copia locale
		os.RemoveAll(c.mirrorDir())
		os.MkdirAll(c.mirrorDir(), 0o700)
		c.st = clientState{Files: map[string]*fstate{}}
		c.saveState()
	}
	c.revoked = false
	err = saveJSON(c.cfgPath(), &c.cfg)
	c.mu.Unlock()
	c.log.Printf("Abbinato al NAS %s come %q", u, c.cfg.DeviceName)
	select {
	case c.pullReset <- struct{}{}:
	default:
	}
	return err
}

func (c *Client) CreateVault(password string) error {
	if len(password) < 10 {
		return errors.New("la password deve avere almeno 10 caratteri")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	m, err := c.fetchMeta(ctx)
	if err != nil {
		return err
	}
	if m != nil {
		return errors.New("la cassaforte esiste già: usa Apri")
	}
	salt := randPassword(32)
	cip, err := rcrypt.New(password, salt)
	if err != nil {
		return err
	}
	chk, _ := cip.Seal([]byte(checkPlain))
	meta := vaultMeta{V: 1, Salt: salt, Check: base64.StdEncoding.EncodeToString(chk), Created: time.Now().Format(time.RFC3339), Format: "rclone-crypt standard, directory_name_encryption=true, password2=salt"}
	b, _ := json.Marshal(&meta)
	resp, err := c.req(ctx, http.MethodPut, "/api/v1/meta", nil, bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return err
	}
	resp.Body.Close()
	writeFileAtomic(c.metaCache(), b, 0o600)
	c.log.Printf("Cassaforte creata")
	return c.Open(password)
}

func (c *Client) Open(password string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var m *vaultMeta
	var err error
	if c.paired() {
		m, err = c.fetchMeta(ctx)
	} else {
		var lm vaultMeta
		if loadJSON(c.metaCache(), &lm) == nil {
			m = &lm
		}
	}
	if err != nil {
		return err
	}
	if m == nil {
		return errors.New("la cassaforte non è ancora stata creata")
	}
	cip, err := rcrypt.New(password, m.Salt)
	if err != nil {
		return err
	}
	chk, _ := base64.StdEncoding.DecodeString(m.Check)
	if plain, err := cip.Open(chk); err != nil || string(plain) != checkPlain {
		return errors.New("password errata")
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cip != nil {
		return nil
	}
	work := c.cfg.WorkDir
	recovering := c.st.Open
	if recovering {
		if _, err := os.Stat(work); err != nil {
			recovering = false
		}
	}
	if !recovering {
		if ents, err := os.ReadDir(work); err == nil && len(ents) > 0 {
			return fmt.Errorf("la cartella %s esiste già e non è vuota: spostala o svuotala", work)
		}
		for _, st := range c.st.Files {
			st.Plain = nil
		}
	}
	if err := os.MkdirAll(work, 0o700); err != nil {
		return err
	}
	c.cip = cip
	c.encCache = map[string]string{}
	c.busy = "apertura"
	defer func() { c.busy = "" }()
	os.WriteFile(filepath.Join(work, readmeName), []byte(readmeText), 0o600)
	markFolder(work)
	var failed int
	for enc, st := range c.st.Files {
		if st.Del || (recovering && st.Plain != nil) {
			continue
		}
		if _, err := os.Stat(c.mirrorPath(enc)); err != nil {
			continue
		}
		if err := c.decryptToWorkLocked(enc, st); err != nil {
			failed++
			c.log.Printf("ERRORE decifratura di un file: %v", err)
		}
	}
	c.st.Open = true
	c.saveState()
	if recovering {
		c.log.Printf("Cassaforte riaperta (ripresa dopo un'interruzione): %s", work)
	} else {
		c.log.Printf("Cassaforte aperta in %s", work)
	}
	if failed > 0 {
		c.lastErr = fmt.Sprintf("%d file non decifrabili (vedi registro)", failed)
	}
	c.kick()
	return nil
}

// Close cifra le ultime modifiche, prova a inviarle e cancella la cartella in chiaro.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.cip == nil {
		open := c.st.Open
		c.mu.Unlock()
		if open {
			return errors.New("la cassaforte era rimasta aperta: inserisci la password e premi Apri, poi chiudi")
		}
		return nil
	}
	c.busy = "chiusura"
	if _, err := c.scanLocked(true); err != nil {
		c.busy = ""
		c.mu.Unlock()
		return fmt.Errorf("non chiudo: %v (chiudi le applicazioni che usano i file e riprova)", err)
	}
	if c.massDel > 0 {
		c.busy = ""
		c.mu.Unlock()
		return fmt.Errorf("ci sono %d eliminazioni in attesa di conferma: confermale o ripristina i file prima di chiudere", c.massDel)
	}
	c.saveState()
	c.mu.Unlock()

	// prova a inviare subito (non obbligatorio: se il NAS è spento resta in coda cifrato)
	if c.paired() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		c.pushOnce(ctx)
		cancel()
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	defer func() { c.busy = "" }()
	err := os.RemoveAll(c.cfg.WorkDir)
	for _, st := range c.st.Files {
		st.Plain = nil
	}
	c.cip = nil
	c.encCache = map[string]string{}
	c.st.Open = false
	c.saveState()
	if err != nil {
		c.lastErr = "alcuni file in chiaro non sono stati cancellati: " + err.Error()
		c.log.Printf("ATTENZIONE: %s", c.lastErr)
		return errors.New(c.lastErr)
	}
	c.log.Printf("Cassaforte chiusa: cartella in chiaro cancellata")
	return nil
}

func (c *Client) ConfirmDeletes() {
	c.mu.Lock()
	c.allowDel = true
	c.mu.Unlock()
}

func (c *Client) OpenFolder() error {
	c.mu.Lock()
	open, work := c.cip != nil, c.cfg.WorkDir
	c.mu.Unlock()
	if !open {
		return errors.New("la cassaforte è chiusa")
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", work)
	case "windows":
		cmd = exec.Command("explorer", work)
	default:
		cmd = exec.Command("xdg-open", work)
	}
	return cmd.Start()
}

func (c *Client) Status() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	var files, pending, deferred int
	for _, st := range c.st.Files {
		if !st.Del {
			files++
		}
		if st.Pending != "" {
			pending++
		}
		if st.Conflict > 0 {
			deferred++
		}
	}
	probs := []string{}
	for k, v := range c.problems {
		probs = append(probs, k+": "+v)
	}
	sort.Strings(probs)
	_, metaErr := os.Stat(c.metaCache())
	return map[string]any{
		"version": Version, "paired": c.cfg.Token != "", "revoked": c.revoked, "hubUrl": c.cfg.HubURL,
		"deviceName": c.cfg.DeviceName, "online": c.online, "lastError": c.lastErr, "lastSync": c.lastSync,
		"open": c.cip != nil, "staleOpen": c.cip == nil && c.st.Open, "workDir": c.cfg.WorkDir,
		"files": files, "pending": pending, "deferredConflicts": deferred, "conflicts": c.conflicts,
		"problems": probs, "massDelete": c.massDel, "busy": c.busy, "vaultKnown": metaErr == nil,
		"log": c.log.Lines(),
	}
}

func (c *Client) SetWorkDir(dir string) error {
	dir = strings.TrimSpace(dir)
	if dir == "" || !filepath.IsAbs(dir) {
		return errors.New("indica un percorso assoluto")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.st.Open {
		return errors.New("chiudi la cassaforte prima di cambiare cartella")
	}
	c.cfg.WorkDir = dir
	return saveJSON(c.cfgPath(), &c.cfg)
}

func (c *Client) run(ctx context.Context) {
	go c.pullLoop(ctx)
	go c.pushLoop(ctx)
	go c.scanLoop(ctx)
	c.mu.Lock()
	if c.st.Open {
		c.log.Printf("La cassaforte era rimasta aperta (riavvio?): apri con la password per riprendere e poi chiudi")
	}
	c.mu.Unlock()
}
