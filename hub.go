package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/scrypt"
)

// Entry descrive un file cifrato conservato dall'hub.
type Entry struct {
	Path  string `json:"p"`
	Rev   int64  `json:"rev"`
	Size  int64  `json:"size"`
	SHA   string `json:"sha"`
	MTime int64  `json:"mtime"`
	Del   bool   `json:"del,omitempty"`
	Seq   int64  `json:"seq"`
	Dev   string `json:"dev,omitempty"`
	// Dir: cartella vuota (nessun contenuto). Una cartella ha una voce solo se non contiene nulla.
	Dir bool `json:"dir,omitempty"`
}

type hubIndex struct {
	Seq     int64             `json:"seq"`
	Entries map[string]*Entry `json:"entries"`
}

type Device struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	TokenHash string    `json:"tokenHash"`
	Created   time.Time `json:"created"`
	LastSeen  time.Time `json:"lastSeen"`
	Version   string    `json:"version,omitempty"` // versione di encluca dichiarata dal client
	// abbinamento in attesa: il codice EL2 contiene una chiave monouso (qui solo l'hash) che il
	// client scambia con il token definitivo. Finché TokenHash è vuoto il dispositivo non è attivo.
	PairHash string    `json:"pairHash,omitempty"`
	PairExp  time.Time `json:"pairExp,omitempty"`
}

// protoVersion è la versione del protocollo tra client e hub. La 2 (encluca 0.2.2) aggiunge
// le voci cartella: l'hub rifiuta con 426 i client che non la dichiarano (X-Encluca-Proto).
const protoVersion = 2

// pairValidity è la durata di un codice di abbinamento non ancora usato.
const pairValidity = 24 * time.Hour

type hubConfig struct {
	AdminHash string       `json:"adminHash"`
	AdminSalt string       `json:"adminSalt"`
	Devices   []*Device    `json:"devices"`
	GDrive    GDriveConfig `json:"gdrive"`
	Lang      string       `json:"lang,omitempty"` // "it" | "en"
	// BrowseEnabled consente di sfogliare la cassaforte dalla pagina del NAS (decifratura nel browser)
	BrowseEnabled bool `json:"browseEnabled,omitempty"`
}

type Hub struct {
	dir         string
	listen      string
	mu          sync.Mutex
	cfg         hubConfig
	idx         hubIndex
	notify      chan struct{}
	sessions    map[string]time.Time
	fingerprint string
	cert        tls.Certificate
	log         *ringLog
	gd          *GDrive
	limiter     *ipLimiter
	scryptSem   chan struct{}
	trash       []trashItem
	latest      string // ultima versione pubblicata (solo per l'avviso: l'hub non si aggiorna da solo)
	latestFail  bool   // errore del controllo già registrato
}

func (h *Hub) vaultDir() string  { return filepath.Join(h.dir, "vault") }
func (h *Hub) tmpDir() string    { return filepath.Join(h.dir, "tmp") }
func (h *Hub) metaPath() string  { return filepath.Join(h.dir, "vault-meta.json") }
func (h *Hub) indexPath() string { return filepath.Join(h.dir, "index.json") }
func (h *Hub) cfgPath() string   { return filepath.Join(h.dir, "hub-config.json") }

func hashAdmin(pw, salt string) string {
	k, _ := scrypt.Key([]byte(pw), []byte(salt), 32768, 8, 1, 32)
	return hex.EncodeToString(k)
}

func newHub(dir, listen string) (*Hub, error) {
	h := &Hub{dir: dir, listen: listen, notify: make(chan struct{}), sessions: map[string]time.Time{},
		limiter: newIPLimiter(), scryptSem: make(chan struct{}, 2)}
	for _, d := range []string{dir, h.vaultDir(), h.tmpDir()} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	h.log = newRingLog(filepath.Join(dir, "encluca-hub.log"), 300)
	// pulizia file temporanei rimasti da un crash
	if ents, err := os.ReadDir(h.tmpDir()); err == nil {
		for _, e := range ents {
			os.Remove(filepath.Join(h.tmpDir(), e.Name()))
		}
	}
	if err := loadJSON(h.cfgPath(), &h.cfg); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf(T("config hub illeggibile: %w"), err)
	}
	setLang(h.cfg.Lang)
	if h.cfg.AdminHash == "" {
		pw := randPassword(16)
		h.setAdminPassword(pw)
		pwFile := filepath.Join(dir, "admin-password.txt")
		os.WriteFile(pwFile, []byte(pw+"\n"), 0o600)
		h.log.Printf(T("Password amministratore iniziale: %s (salvata anche in %s)"), pw, pwFile)
	}
	if err := loadJSON(h.indexPath(), &h.idx); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf(T("indice illeggibile: %w"), err)
		}
		if err := h.rebuildIndex(); err != nil {
			return nil, err
		}
	}
	if h.idx.Entries == nil {
		h.idx.Entries = map[string]*Entry{}
	}
	if err := h.loadOrCreateCert(); err != nil {
		return nil, err
	}
	h.loadTrash()
	h.gd = newGDrive(h)
	return h, h.saveCfg()
}

func (h *Hub) setAdminPassword(pw string) {
	h.cfg.AdminSalt = randHex(16)
	h.cfg.AdminHash = hashAdmin(pw, h.cfg.AdminSalt)
}

func (h *Hub) saveCfg() error   { return saveJSON(h.cfgPath(), &h.cfg) }
func (h *Hub) saveIndex() error { return saveJSON(h.indexPath(), &h.idx) }

// rebuildIndex ricostruisce l'indice dai file presenti (se index.json è andato perso).
func (h *Hub) rebuildIndex() error {
	h.idx = hubIndex{Entries: map[string]*Entry{}}
	err := filepath.WalkDir(h.vaultDir(), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(h.vaultDir(), p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			// una cartella senza contenuto è una cartella vuota della cassaforte
			if ents, err := os.ReadDir(p); err == nil && len(ents) == 0 && p != h.vaultDir() && validEncPath(rel) {
				h.idx.Seq++
				h.idx.Entries[rel] = &Entry{Path: rel, Rev: 1, MTime: 0, Seq: h.idx.Seq, Dir: true}
			}
			return nil
		}
		if !validEncPath(rel) {
			return nil
		}
		sum, size, err := hashFile(p)
		if err != nil {
			return err
		}
		info, _ := d.Info()
		h.idx.Seq++
		h.idx.Entries[rel] = &Entry{Path: rel, Rev: 1, Size: size, SHA: sum, MTime: info.ModTime().UnixNano(), Seq: h.idx.Seq}
		return nil
	})
	if err != nil {
		return err
	}
	if len(h.idx.Entries) > 0 {
		h.log.Printf(T("Indice ricostruito: %d file"), len(h.idx.Entries))
	}
	return h.saveIndex()
}

func (h *Hub) loadOrCreateCert() error {
	certFile, keyFile := filepath.Join(h.dir, "tls-cert.pem"), filepath.Join(h.dir, "tls-key.pem")
	if _, err := os.Stat(certFile); errors.Is(err, fs.ErrNotExist) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return err
		}
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(time.Now().UnixNano()),
			Subject:      pkix.Name{CommonName: "encluca-hub"},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().AddDate(30, 0, 0),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			DNSNames:     []string{"encluca-hub"},
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
		if err != nil {
			return err
		}
		kb, _ := x509.MarshalECPrivateKey(key)
		writeFileAtomic(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
		writeFileAtomic(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600)
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return err
	}
	h.cert = cert
	sum := sha256.Sum256(cert.Certificate[0])
	h.fingerprint = hex.EncodeToString(sum[:])
	return nil
}

// broadcast sveglia i client in attesa di modifiche (long-poll). Va chiamata con h.mu acquisito.
func (h *Hub) broadcast() {
	close(h.notify)
	h.notify = make(chan struct{})
	h.gd.changed()
}

// ---------- autenticazione dispositivi ----------

func (h *Hub) deviceAuth(r *http.Request) *Device {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if tok == "" {
		return nil
	}
	sum := sha256.Sum256([]byte(tok))
	th := hex.EncodeToString(sum[:])
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, d := range h.cfg.Devices {
		if d.TokenHash != "" && subtle.ConstantTimeCompare([]byte(d.TokenHash), []byte(th)) == 1 {
			if time.Since(d.LastSeen) > time.Minute {
				d.LastSeen = time.Now()
				h.saveCfg()
			}
			return d
		}
	}
	return nil
}

func (h *Hub) withDevice(fn func(http.ResponseWriter, *http.Request, *Device)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d := h.deviceAuth(r)
		if d == nil {
			writeErr(w, http.StatusUnauthorized, T("dispositivo non autorizzato"))
			return
		}
		if p, err := strconv.Atoi(r.Header.Get("X-Encluca-Proto")); err != nil || p < protoVersion {
			writeErr(w, http.StatusUpgradeRequired, T("questo dispositivo usa una versione di encluca troppo vecchia per questo NAS: aggiornalo alla 0.2.2 o successiva"))
			return
		}
		if v := r.Header.Get("X-Encluca-Version"); v != "" {
			h.mu.Lock()
			if d.Version != v {
				d.Version = v
				h.saveCfg()
			}
			h.mu.Unlock()
		}
		fn(w, r, d)
	}
}

// ---------- API dispositivi ----------

// pruneExpiredPairsLocked toglie i dispositivi mai abbinati il cui codice è scaduto.
func (h *Hub) pruneExpiredPairsLocked() bool {
	out := h.cfg.Devices[:0]
	pruned := false
	for _, d := range h.cfg.Devices {
		if d.TokenHash == "" && time.Now().After(d.PairExp) {
			pruned = true
			continue
		}
		out = append(out, d)
	}
	h.cfg.Devices = out
	return pruned
}

// apiPair scambia la chiave monouso del codice di abbinamento con il token del dispositivo.
// Dopo l'uso (o dopo 24 ore) il codice non vale più: chi lo vede in seguito non può usarlo.
func (h *Hub) apiPair(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, T("metodo non ammesso"))
		return
	}
	ip := clientIP(r)
	if wait := h.limiter.blocked(ip); wait > 0 {
		tooMany(w, wait)
		return
	}
	var body struct{ Key string }
	readBody(r, &body)
	sum := sha256Hex([]byte(body.Key))
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pruneExpiredPairsLocked()
	var dev *Device
	for _, d := range h.cfg.Devices {
		if body.Key != "" && d.TokenHash == "" && subtle.ConstantTimeCompare([]byte(d.PairHash), []byte(sum)) == 1 {
			dev = d
		}
	}
	if dev == nil {
		h.limiter.fail(ip)
		h.log.Printf(T("Codice di abbinamento non valido, già usato o scaduto (da %s)"), ip)
		writeErr(w, http.StatusGone, T("codice di abbinamento già usato o scaduto: generane uno nuovo dalla pagina del NAS"))
		return
	}
	tok := randHex(32)
	dev.TokenHash, dev.PairHash, dev.PairExp = sha256Hex([]byte(tok)), "", time.Time{}
	dev.LastSeen = time.Now()
	if err := h.saveCfg(); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.limiter.ok(ip)
	h.log.Printf(T("Dispositivo abbinato: %q"), dev.Name)
	writeJSON(w, http.StatusOK, map[string]string{"token": tok, "name": dev.Name})
}

func (h *Hub) apiMeta(w http.ResponseWriter, r *http.Request, d *Device) {
	switch r.Method {
	case http.MethodGet:
		b, err := os.ReadFile(h.metaPath())
		if err != nil {
			writeErr(w, http.StatusNotFound, T("cassaforte non ancora creata"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	case http.MethodPut:
		b, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		if err != nil || !json.Valid(b) {
			writeErr(w, http.StatusBadRequest, T("metadati non validi"))
			return
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, err := os.Stat(h.metaPath()); err == nil {
			writeErr(w, http.StatusConflict, T("la cassaforte esiste già"))
			return
		}
		if err := writeFileAtomic(h.metaPath(), b, 0o600); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		h.log.Printf(T("Cassaforte creata dal dispositivo %q"), d.Name)
		h.gd.changed()
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	default:
		writeErr(w, http.StatusMethodNotAllowed, T("metodo non ammesso"))
	}
}

func (h *Hub) apiChanges(w http.ResponseWriter, r *http.Request, d *Device) {
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	wait, _ := strconv.Atoi(r.URL.Query().Get("wait"))
	if wait > 55 {
		wait = 55
	}
	collect := func() (int64, []*Entry, chan struct{}) {
		h.mu.Lock()
		defer h.mu.Unlock()
		var out []*Entry
		for _, e := range h.idx.Entries {
			if e.Seq > since {
				c := *e
				out = append(out, &c)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
		return h.idx.Seq, out, h.notify
	}
	seq, out, ch := collect()
	if len(out) == 0 && wait > 0 && since >= seq {
		select {
		case <-ch:
		case <-time.After(time.Duration(wait) * time.Second):
		case <-r.Context().Done():
			return
		}
		seq, out, _ = collect()
	}
	if out == nil {
		out = []*Entry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"seq": seq, "entries": out})
}

func (h *Hub) apiEntry(w http.ResponseWriter, r *http.Request, d *Device) {
	p := r.URL.Query().Get("p")
	if !validEncPath(p) {
		writeErr(w, http.StatusBadRequest, T("percorso non valido"))
		return
	}
	h.mu.Lock()
	e := h.idx.Entries[p]
	var c Entry
	if e != nil {
		c = *e
	}
	h.mu.Unlock()
	if e == nil {
		writeErr(w, http.StatusNotFound, T("non trovato"))
		return
	}
	writeJSON(w, http.StatusOK, &c)
}

func (h *Hub) apiFile(w http.ResponseWriter, r *http.Request, d *Device) {
	p := r.URL.Query().Get("p")
	if !validEncPath(p) {
		writeErr(w, http.StatusBadRequest, T("percorso non valido"))
		return
	}
	full := filepath.Join(h.vaultDir(), filepath.FromSlash(p))
	switch r.Method {
	case http.MethodGet:
		h.mu.Lock()
		e := h.idx.Entries[p]
		var c Entry
		if e != nil {
			c = *e
		}
		f, err := os.Open(full)
		h.mu.Unlock()
		if e == nil || e.Del || e.Dir || err != nil {
			if f != nil {
				f.Close()
			}
			writeErr(w, http.StatusNotFound, T("non trovato"))
			return
		}
		defer f.Close()
		w.Header().Set("X-Rev", strconv.FormatInt(c.Rev, 10))
		w.Header().Set("X-Sha", c.SHA)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.FormatInt(c.Size, 10))
		io.Copy(w, f)

	case http.MethodPut:
		base, _ := strconv.ParseInt(r.URL.Query().Get("base"), 10, 64)
		mtime, _ := strconv.ParseInt(r.URL.Query().Get("mtime"), 10, 64)
		if r.URL.Query().Get("dir") == "1" {
			h.putDir(w, p, full, base, mtime, d)
			return
		}
		wantSHA := r.URL.Query().Get("sha")
		tmp, err := os.CreateTemp(h.tmpDir(), "up-*")
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		tmpName := tmp.Name()
		defer os.Remove(tmpName)
		hs := sha256.New()
		size, err := io.Copy(io.MultiWriter(tmp, hs), r.Body)
		if err == nil {
			err = tmp.Sync()
		}
		tmp.Close()
		if err != nil {
			writeErr(w, http.StatusBadRequest, T("upload interrotto"))
			return
		}
		sum := hex.EncodeToString(hs.Sum(nil))
		if wantSHA != "" && wantSHA != sum {
			writeErr(w, http.StatusBadRequest, T("checksum non corrispondente"))
			return
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		cur := h.idx.Entries[p]
		var curRev int64
		if cur != nil {
			curRev = cur.Rev
		}
		if base != curRev {
			c := Entry{Path: p}
			if cur != nil {
				c = *cur
			}
			writeJSON(w, http.StatusConflict, &c)
			return
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if err := os.Rename(tmpName, full); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if mtime > 0 {
			t := time.Unix(0, mtime)
			os.Chtimes(full, t, t)
		}
		h.idx.Seq++
		e := &Entry{Path: p, Rev: curRev + 1, Size: size, SHA: sum, MTime: mtime, Seq: h.idx.Seq, Dev: d.ID}
		h.idx.Entries[p] = e
		if err := h.saveIndex(); err != nil {
			h.log.Printf(T("ERRORE salvataggio indice: %v"), err)
		}
		h.broadcast()
		c := *e
		writeJSON(w, http.StatusOK, &c)

	case http.MethodDelete:
		base, _ := strconv.ParseInt(r.URL.Query().Get("base"), 10, 64)
		h.mu.Lock()
		defer h.mu.Unlock()
		cur := h.idx.Entries[p]
		if cur == nil || cur.Rev != base {
			if cur == nil {
				writeErr(w, http.StatusNotFound, T("non trovato"))
				return
			}
			c := *cur
			writeJSON(w, http.StatusConflict, &c)
			return
		}
		if cur.Del {
			c := *cur
			writeJSON(w, http.StatusOK, &c)
			return
		}
		if cur.Dir {
			os.Remove(full) // se nel frattempo contiene altro, resta
			h.trashDirLocked(cur, d.ID)
		} else if err := h.trashFileLocked(full, cur, d.ID); err != nil && !errors.Is(err, fs.ErrNotExist) {
			h.log.Printf(T("ERRORE spostamento nel cestino: %v"), err)
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		// rimuove le cartelle rimaste vuote, tranne quelle che sono voci cartella vive
		for dir := filepath.Dir(full); dir != h.vaultDir() && strings.HasPrefix(dir, h.vaultDir()); dir = filepath.Dir(dir) {
			rel, _ := filepath.Rel(h.vaultDir(), dir)
			if h.hasLiveDirLocked(filepath.ToSlash(rel)) || os.Remove(dir) != nil {
				break
			}
		}
		h.idx.Seq++
		cur.Rev++
		cur.Del = true
		cur.Size, cur.SHA = 0, ""
		cur.Seq = h.idx.Seq
		cur.Dev = d.ID
		h.saveIndex()
		h.broadcast()
		c := *cur
		writeJSON(w, http.StatusOK, &c)
	default:
		writeErr(w, http.StatusMethodNotAllowed, T("metodo non ammesso"))
	}
}

// putDir crea (o ricrea) la voce di una cartella vuota: nessun contenuto, solo la cartella sul disco.
func (h *Hub) putDir(w http.ResponseWriter, p, full string, base, mtime int64, d *Device) {
	h.mu.Lock()
	defer h.mu.Unlock()
	cur := h.idx.Entries[p]
	var curRev int64
	if cur != nil {
		curRev = cur.Rev
	}
	if base != curRev {
		c := Entry{Path: p}
		if cur != nil {
			c = *cur
		}
		writeJSON(w, http.StatusConflict, &c)
		return
	}
	if cur != nil && !cur.Del && !cur.Dir {
		// al posto di un file ora c'è una cartella vuota: il file va nel cestino, non perso
		if err := h.trashFileLocked(full, cur, d.ID); err != nil && !errors.Is(err, fs.ErrNotExist) {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if err := os.MkdirAll(full, 0o700); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.idx.Seq++
	e := &Entry{Path: p, Rev: curRev + 1, MTime: mtime, Seq: h.idx.Seq, Dev: d.ID, Dir: true}
	h.idx.Entries[p] = e
	if err := h.saveIndex(); err != nil {
		h.log.Printf(T("ERRORE salvataggio indice: %v"), err)
	}
	h.broadcast()
	c := *e
	writeJSON(w, http.StatusOK, &c)
}

// hasLiveDirLocked dice se enc è una voce cartella non cancellata.
func (h *Hub) hasLiveDirLocked(enc string) bool {
	e := h.idx.Entries[enc]
	return e != nil && e.Dir && !e.Del
}

// ---------- amministrazione ----------

func (h *Hub) adminOK(r *http.Request) bool {
	c, err := r.Cookie("el_session")
	if err != nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	exp, ok := h.sessions[c.Value]
	if !ok || time.Now().After(exp) {
		delete(h.sessions, c.Value)
		return false
	}
	return true
}

func (h *Hub) withAdmin(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Header.Get("X-Encluca") != "1" {
			writeErr(w, http.StatusForbidden, T("richiesta non valida"))
			return
		}
		if !h.adminOK(r) {
			writeErr(w, http.StatusUnauthorized, T("accesso richiesto"))
			return
		}
		fn(w, r)
	}
}

func readBody(r *http.Request, v any) error {
	return json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v)
}

func (h *Hub) adminLogin(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Encluca") != "1" || r.Method != http.MethodPost {
		writeErr(w, http.StatusForbidden, T("richiesta non valida"))
		return
	}
	ip := clientIP(r)
	if wait := h.limiter.blocked(ip); wait > 0 {
		tooMany(w, wait)
		return
	}
	var body struct{ Password string }
	readBody(r, &body)
	if !h.checkAdmin(body.Password) {
		h.limiter.fail(ip)
		h.log.Printf(T("Accesso amministratore fallito da %s"), ip)
		writeErr(w, http.StatusUnauthorized, T("password errata"))
		return
	}
	h.limiter.ok(ip)
	sid := randHex(32)
	h.mu.Lock()
	h.sessions[sid] = time.Now().Add(12 * time.Hour)
	h.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "el_session", Value: sid, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: 12 * 3600})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Hub) adminLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("el_session"); err == nil {
		h.mu.Lock()
		delete(h.sessions, c.Value)
		h.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: "el_session", Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Hub) adminStatus(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	var files int
	var size int64
	for _, e := range h.idx.Entries {
		if !e.Del && !e.Dir {
			files++
			size += e.Size
		}
	}
	if h.pruneExpiredPairsLocked() {
		h.saveCfg()
	}
	devs := make([]map[string]any, 0, len(h.cfg.Devices))
	for _, d := range h.cfg.Devices {
		outdated := d.TokenHash != "" && (d.Version == "" || newerVersion("0.2.2", d.Version))
		devs = append(devs, map[string]any{"id": d.ID, "name": d.Name, "created": d.Created, "lastSeen": d.LastSeen, "pending": d.TokenHash == "", "version": d.Version, "outdated": outdated})
	}
	seq := h.idx.Seq
	browse := h.cfg.BrowseEnabled
	trash := h.trashStatsLocked()
	latest := h.latest
	h.mu.Unlock()
	_, metaErr := os.Stat(h.metaPath())
	_, pwFileErr := os.Stat(filepath.Join(h.dir, "admin-password.txt"))
	res := map[string]any{
		"version": Version, "files": files, "bytes": size, "seq": seq,
		"vaultCreated": metaErr == nil, "devices": devs, "fingerprint": h.fingerprint, "browseEnabled": browse,
		"defaultPassword": pwFileErr == nil, "lang": getLang(),
		"gdrive": h.gd.status(), "trash": trash, "log": h.log.Lines(),
	}
	if newerVersion(latest, Version) {
		res["latestVersion"] = latest
	}
	writeJSON(w, http.StatusOK, res)
}

func (h *Hub) adminDevices(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		var body struct{ Name, HubURL string }
		readBody(r, &body)
		body.Name = strings.TrimSpace(body.Name)
		if body.Name == "" {
			writeErr(w, http.StatusBadRequest, T("nome dispositivo obbligatorio"))
			return
		}
		key := randHex(32)
		d := &Device{ID: randHex(6), Name: body.Name, PairHash: sha256Hex([]byte(key)), PairExp: time.Now().Add(pairValidity), Created: time.Now()}
		h.mu.Lock()
		h.pruneExpiredPairsLocked()
		h.cfg.Devices = append(h.cfg.Devices, d)
		err := h.saveCfg()
		h.mu.Unlock()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		u := strings.TrimRight(strings.TrimSpace(body.HubURL), "/")
		if u == "" {
			u = "https://" + r.Host
		}
		code, _ := json.Marshal(map[string]string{"u": u, "k": key, "f": h.fingerprint})
		h.log.Printf(T("Nuovo dispositivo abbinabile: %q"), d.Name)
		writeJSON(w, http.StatusOK, map[string]string{"code": "EL2-" + base64.RawURLEncoding.EncodeToString(code), "id": d.ID})
	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		h.mu.Lock()
		out := h.cfg.Devices[:0]
		var removed string
		for _, d := range h.cfg.Devices {
			if d.ID == id {
				removed = d.Name
				continue
			}
			out = append(out, d)
		}
		h.cfg.Devices = out
		h.saveCfg()
		h.mu.Unlock()
		if removed != "" {
			h.log.Printf(T("Dispositivo revocato: %q"), removed)
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	default:
		writeErr(w, http.StatusMethodNotAllowed, T("metodo non ammesso"))
	}
}

func (h *Hub) adminLang(w http.ResponseWriter, r *http.Request) {
	var body struct{ Lang string }
	readBody(r, &body)
	if !validLang(body.Lang) {
		writeErr(w, http.StatusBadRequest, "lingua non supportata / unsupported language")
		return
	}
	h.mu.Lock()
	h.cfg.Lang = body.Lang
	setLang(body.Lang)
	err := h.saveCfg()
	h.mu.Unlock()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Hub) adminPassword(w http.ResponseWriter, r *http.Request) {
	var body struct{ Old, New string }
	readBody(r, &body)
	if len([]rune(body.New)) < minPasswordLen {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf(T("la nuova password deve avere almeno %d caratteri"), minPasswordLen))
		return
	}
	ip := clientIP(r)
	if wait := h.limiter.blocked(ip); wait > 0 {
		tooMany(w, wait)
		return
	}
	if !h.checkAdmin(body.Old) {
		h.limiter.fail(ip)
		writeErr(w, http.StatusUnauthorized, T("password attuale errata"))
		return
	}
	salt := randHex(16)
	h.scryptSem <- struct{}{}
	hash := hashAdmin(body.New, salt)
	<-h.scryptSem
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.AdminSalt, h.cfg.AdminHash = salt, hash
	if err := h.saveCfg(); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	os.Remove(filepath.Join(h.dir, "admin-password.txt"))
	// chi conosceva la vecchia password perde l'accesso: resta valida solo la sessione
	// che ha fatto il cambio (gli altri browser devono rientrare con la nuova)
	cur, _ := r.Cookie("el_session")
	for sid := range h.sessions {
		if cur == nil || sid != cur.Value {
			delete(h.sessions, sid)
		}
	}
	h.log.Print(T("Password amministratore cambiata: le altre sessioni sono state chiuse"))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline' 'wasm-unsafe-eval'; img-src 'self' data: blob:; connect-src 'self'; form-action 'self'; base-uri 'none'")
		next.ServeHTTP(w, r)
	})
}

func (h *Hub) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/ping", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"app": "encluca-hub", "version": Version})
	})
	mux.HandleFunc("/api/v1/pair", h.apiPair)
	mux.HandleFunc("/api/v1/meta", h.withDevice(h.apiMeta))
	mux.HandleFunc("/api/v1/changes", h.withDevice(h.apiChanges))
	mux.HandleFunc("/api/v1/entry", h.withDevice(h.apiEntry))
	mux.HandleFunc("/api/v1/file", h.withDevice(h.apiFile))

	mux.HandleFunc("/admin/api/login", h.adminLogin)
	mux.HandleFunc("/admin/api/logout", h.adminLogout)
	mux.HandleFunc("/admin/api/status", h.withAdmin(h.adminStatus))
	mux.HandleFunc("/admin/api/devices", h.withAdmin(h.adminDevices))
	mux.HandleFunc("/admin/api/password", h.withAdmin(h.adminPassword))
	mux.HandleFunc("/admin/api/lang", h.withAdmin(h.adminLang))
	mux.HandleFunc("/admin/api/browse", h.withAdmin(h.adminBrowseToggle))
	mux.HandleFunc("/admin/api/trash/restore", h.withAdmin(h.adminTrashRestore))
	mux.HandleFunc("/admin/api/vault/meta", h.withBrowse(h.apiBrowseMeta))
	mux.HandleFunc("/admin/api/vault/list", h.withBrowse(h.apiBrowseList))
	mux.HandleFunc("/admin/api/vault/file", h.withBrowse(h.apiBrowseFile))
	mux.HandleFunc("/admin/wasm_exec.js", serveStatic("web/wasm_exec.js", "text/javascript; charset=utf-8"))
	mux.HandleFunc("/admin/vault.wasm", serveStatic("web/vault.wasm", "application/wasm"))
	mux.HandleFunc("/admin/api/gdrive/config", h.withAdmin(h.gd.apiConfig))
	mux.HandleFunc("/admin/api/gdrive/auth", h.withAdmin(h.gd.apiAuth))
	mux.HandleFunc("/admin/api/gdrive/run", h.withAdmin(h.gd.apiRun))
	mux.HandleFunc("/admin/api/gdrive/disconnect", h.withAdmin(h.gd.apiDisconnect))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		serveUI(w, "web/hub.html")
	})
	return securityHeaders(mux)
}

func (h *Hub) run(ctx context.Context) error {
	srv := &http.Server{
		Addr:              h.listen,
		Handler:           h.handler(),
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{h.cert}, MinVersion: tls.VersionTLS12},
		ReadHeaderTimeout: 20 * time.Second,
	}
	gdDone := make(chan struct{})
	go func() {
		defer close(gdDone)
		h.gd.loop(ctx)
	}()
	go h.latestLoop(ctx)
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	}()
	h.log.Printf(T("Encluca hub %s in ascolto su https://%s (impronta certificato %s…)"), Version, h.listen, h.fingerprint[:16])
	err := srv.ListenAndServeTLS("", "")
	if errors.Is(err, http.ErrServerClosed) {
		// arresto ordinato: il backup interrompe subito il file in corso e salva il suo stato
		// (di solito in meno di un secondo). L'attesa è limitata a 3 secondi, così l'arresto
		// dell'app o di TrueNAS non viene mai trattenuto: Docker ne concede 10.
		select {
		case <-gdDone:
		case <-time.After(3 * time.Second):
		}
		return nil
	}
	return err
}
