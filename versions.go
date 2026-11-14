package main

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"encluca/internal/rcrypt"
)

// Versioni precedenti dei file.
//
// Quando un dispositivo sovrascrive un file, l'hub conserva la versione precedente (sempre
// cifrata) in versions/, con un indice in versions.json: stesso modello del cestino, che invece
// conserva i file eliminati. Servono a rimediare a una modifica sbagliata o a un ransomware che
// cifra i file su un PC: dalla pagina del NAS si riporta tutto com'era a una certa data, senza
// bisogno di conoscere i nomi in chiaro. Le versioni non vanno nel backup su Drive.

const (
	defaultVersionsKeep   = 5
	defaultVersionsMaxAge = 90 // giorni
)

// versionItem descrive una versione conservata. Since e Replaced delimitano il periodo in cui
// è stata la versione corrente: così si può dire quale contenuto aveva un file a una certa data.
type versionItem struct {
	Path     string `json:"p"`
	File     string `json:"file"` // nome del file in versions/
	Size     int64  `json:"size"`
	SHA      string `json:"sha"`
	MTime    int64  `json:"mtime"`
	Rev      int64  `json:"rev"`
	Since    int64  `json:"since"`    // unix nano; 0 = non noto (da sempre)
	Replaced int64  `json:"replaced"` // unix nano
	Dev      string `json:"dev,omitempty"`
}

// VersionsConfig: Keep = versioni conservate per ogni file (0 = disattivato);
// MaxAgeDays = giorni dopo i quali una versione sostituita viene eliminata (0 = mai).
type VersionsConfig struct {
	Keep       int `json:"keep"`
	MaxAgeDays int `json:"maxAgeDays"`
}

func (h *Hub) versionsDir() string       { return filepath.Join(h.dir, "versions") }
func (h *Hub) versionsIndexPath() string { return filepath.Join(h.dir, "versions.json") }

func (h *Hub) versionsCfgLocked() VersionsConfig {
	if h.cfg.Versions == nil {
		return VersionsConfig{Keep: defaultVersionsKeep, MaxAgeDays: defaultVersionsMaxAge}
	}
	return *h.cfg.Versions
}

func (h *Hub) loadVersions() {
	if err := loadJSON(h.versionsIndexPath(), &h.versions); err != nil && !errors.Is(err, fs.ErrNotExist) {
		h.log.Printf(T("ERRORE lettura delle versioni: %v"), err)
	}
	os.MkdirAll(h.versionsDir(), 0o700)
	h.mu.Lock()
	h.pruneVersionsLocked()
	h.mu.Unlock()
}

func (h *Hub) saveVersionsLocked() {
	if err := saveJSON(h.versionsIndexPath(), &h.versions); err != nil {
		h.log.Printf(T("ERRORE salvataggio delle versioni: %v"), err)
	}
}

// keepVersionLocked conserva come versione il file corrente di cur (che sta per essere
// sostituito). Con force=false non fa nulla se le versioni sono disattivate.
//
// Il file corrente non viene mai spostato: se ne crea un collegamento fisico (o, se il disco
// non lo permette, una copia). Così un arresto a metà lascia al massimo una versione in più,
// mai un file mancante dalla cassaforte.
func (h *Hub) keepVersionLocked(full string, cur *Entry, now int64, force bool) error {
	if !force && h.versionsCfgLocked().Keep <= 0 {
		return nil
	}
	if err := h.addVersionLocked(full, cur, now); err != nil {
		return err
	}
	h.pruneVersionsLocked()
	h.saveVersionsLocked()
	return nil
}

// addVersionLocked aggiunge all'elenco il file corrente di cur, senza pulizia né salvataggio.
func (h *Hub) addVersionLocked(full string, cur *Entry, now int64) error {
	name := randHex(16)
	dst := filepath.Join(h.versionsDir(), name)
	if err := os.Link(full, dst); err != nil {
		if err := copyFileSync(full, dst, 0o600); err != nil {
			os.Remove(dst)
			return err
		}
	}
	h.versions = append(h.versions, versionItem{Path: cur.Path, File: name, Size: cur.Size, SHA: cur.SHA, MTime: cur.MTime,
		Rev: cur.Rev, Since: cur.At, Replaced: now, Dev: cur.Dev})
	return nil
}

// pruneVersionsLocked elimina le versioni scadute per età e quelle oltre il numero massimo per
// file (restano le più recenti). Con Keep=0 le versioni già presenti restano fino alla scadenza.
func (h *Hub) pruneVersionsLocked() {
	cfg := h.versionsCfgLocked()
	var limit int64
	if cfg.MaxAgeDays > 0 {
		limit = time.Now().Add(-time.Duration(cfg.MaxAgeDays) * 24 * time.Hour).UnixNano()
	}
	// dalla più recente alla più vecchia, per contare quante tenerne per ogni file
	idx := make([]int, len(h.versions))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return h.versions[idx[a]].Replaced > h.versions[idx[b]].Replaced })
	kept := map[string]int{}
	drop := map[int]bool{}
	for _, i := range idx {
		v := h.versions[i]
		switch {
		case limit > 0 && v.Replaced < limit:
			drop[i] = true
		case cfg.Keep > 0 && kept[v.Path] >= cfg.Keep:
			drop[i] = true
		default:
			kept[v.Path]++
		}
	}
	if len(drop) == 0 {
		return
	}
	out := h.versions[:0]
	for i, v := range h.versions {
		if drop[i] {
			os.Remove(filepath.Join(h.versionsDir(), v.File))
			continue
		}
		out = append(out, v)
	}
	h.versions = out
	h.saveVersionsLocked()
}

func (h *Hub) versionsStatsLocked() map[string]any {
	var size int64
	for _, v := range h.versions {
		size += v.Size
	}
	cfg := h.versionsCfgLocked()
	return map[string]any{"files": len(h.versions), "bytes": size, "keep": cfg.Keep, "maxAgeDays": cfg.MaxAgeDays}
}

// versionAtLocked restituisce l'indice della versione di path che era corrente all'istante at:
// tra quelle sostituite dopo at, la prima a essere stata sostituita, purché esistesse già
// (Since <= at; Since 0 = da sempre). -1 se non c'è (file nato dopo, o versione già eliminata).
func (h *Hub) versionAtLocked(path string, at int64) int {
	best := -1
	for i, v := range h.versions {
		if v.Path == path && v.Replaced > at && (best < 0 || v.Replaced < h.versions[best].Replaced) {
			best = i
		}
	}
	if best >= 0 && h.versions[best].Since > at {
		return -1
	}
	return best
}

// restoreVersionLocked rimette nella cassaforte il contenuto della versione h.versions[i].
// La versione resta nell'elenco (la storia non si consuma: si può tornare più volte alla stessa
// data) e il contenuto attuale diventa a sua volta una versione, anche con le versioni
// disattivate: un ripristino si può sempre annullare. La nuova revisione arriva ai dispositivi
// come una normale modifica. Chi chiama salva l'indice e avvisa i dispositivi.
func (h *Hub) restoreVersionLocked(i int, now int64) error {
	v := h.versions[i]
	full := filepath.Join(h.vaultDir(), filepath.FromSlash(v.Path))
	// copia della versione (collegamento fisico, o copia vera se il disco non lo permette)
	tmp := filepath.Join(h.tmpDir(), "ver-"+randHex(8))
	src := filepath.Join(h.versionsDir(), v.File)
	if err := os.Link(src, tmp); err != nil {
		if err := copyFileSync(src, tmp, 0o600); err != nil {
			os.Remove(tmp)
			return err
		}
	}
	defer os.Remove(tmp)
	cur := h.idx.Entries[v.Path]
	var rev int64
	if cur != nil {
		rev = cur.Rev
		switch {
		case cur.Del:
		case cur.Dir:
			os.Remove(full) // al posto del file c'era una cartella vuota
		case cur.SHA != v.SHA:
			if err := h.addVersionLocked(full, cur, now); err != nil {
				return err
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		return err
	}
	if err := os.Rename(tmp, full); err != nil {
		return err
	}
	if v.MTime > 0 {
		t := time.Unix(0, v.MTime)
		os.Chtimes(full, t, t)
	}
	h.idx.Seq++
	h.idx.Entries[v.Path] = &Entry{Path: v.Path, Rev: rev + 1, Size: v.Size, SHA: v.SHA, MTime: v.MTime, Seq: h.idx.Seq, Dev: "versions", At: now}
	h.pruneVersionsLocked()
	h.saveVersionsLocked()
	return nil
}

// restoreAtLocked riporta la cassaforte a com'era all'istante at: i file eliminati dopo at
// tornano dal cestino, e ogni file modificato dopo at torna alla versione che aveva allora.
// I file creati dopo at restano dove sono. Con dry=true conta soltanto, senza cambiare nulla.
func (h *Hub) restoreAtLocked(at int64, dry bool) (changed, undeleted int) {
	if dry {
		undeleted = len(h.trashCandidatesLocked(at))
	} else {
		undeleted = h.restoreTrashLocked(at)
	}
	var paths []string
	for p, e := range h.idx.Entries {
		// At == 0: voce di prima della 0.2.5, non si sa quando è stata modificata: non si tocca
		if !e.Del && !e.Dir && e.At > at {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	now := time.Now().UnixNano()
	for _, p := range paths {
		i := h.versionAtLocked(p, at)
		if i < 0 {
			continue
		}
		if dry {
			changed++
			continue
		}
		if err := h.restoreVersionLocked(i, now); err != nil {
			h.log.Printf(T("ERRORE ripristino di una versione: %v"), err)
			continue
		}
		changed++
	}
	if !dry && changed > 0 {
		if err := h.saveIndex(); err != nil {
			h.log.Printf(T("ERRORE salvataggio indice: %v"), err)
		}
		h.broadcast()
	}
	return changed, undeleted
}

// ---- API amministratore ----

// adminVersionsConfig: POST {Keep, MaxAgeDays}.
func (h *Hub) adminVersionsConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, T("metodo non ammesso"))
		return
	}
	var body VersionsConfig
	if err := readBody(r, &body); err != nil || body.Keep < 0 || body.Keep > 100 || body.MaxAgeDays < 0 || body.MaxAgeDays > 3650 {
		writeErr(w, http.StatusBadRequest, T("valori non validi: versioni da 0 a 100, giorni da 0 a 3650"))
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.Versions = &body
	if err := h.saveCfg(); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.pruneVersionsLocked()
	h.log.Printf(T("Versioni precedenti: se ne conservano %d per file, per %d giorni"), body.Keep, body.MaxAgeDays)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// adminVersionsRestoreAt: POST {At: RFC3339, DryRun} riporta la cassaforte a com'era a quella data.
func (h *Hub) adminVersionsRestoreAt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, T("metodo non ammesso"))
		return
	}
	var body struct {
		At     string
		DryRun bool
	}
	readBody(r, &body)
	at, err := time.Parse(time.RFC3339Nano, body.At)
	if err != nil || at.After(time.Now()) {
		writeErr(w, http.StatusBadRequest, T("data non valida: indica un momento nel passato"))
		return
	}
	h.mu.Lock()
	changed, undeleted := h.restoreAtLocked(at.UnixNano(), body.DryRun)
	h.mu.Unlock()
	if !body.DryRun {
		h.log.Printf(T("Ripristino a %s: %d file riportati alla versione precedente, %d file eliminati recuperati"), at.Local().Format("2006-01-02 15:04"), changed, undeleted)
	}
	writeJSON(w, http.StatusOK, map[string]int{"changed": changed, "undeleted": undeleted})
}

// apiBrowseVersions: GET ?p=<percorso cifrato> elenca le versioni conservate di un file.
func (h *Hub) apiBrowseVersions(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("p")
	if !validEncPath(p) {
		writeErr(w, http.StatusBadRequest, T("percorso non valido"))
		return
	}
	type item struct {
		ID       string `json:"id"`
		Size     int64  `json:"size"` // dimensione in chiaro
		MTime    int64  `json:"mtime"`
		Since    int64  `json:"since"`
		Replaced int64  `json:"replaced"`
	}
	out := []item{}
	h.mu.Lock()
	for _, v := range h.versions {
		if v.Path == p {
			out = append(out, item{v.File, rcrypt.DecryptedSize(v.Size), v.MTime, v.Since, v.Replaced})
		}
	}
	h.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Replaced > out[j].Replaced })
	writeJSON(w, http.StatusOK, out)
}

// apiBrowseVersionRestore: POST {P, ID} ripristina una versione di un file.
func (h *Hub) apiBrowseVersionRestore(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, T("metodo non ammesso"))
		return
	}
	var body struct{ P, ID string }
	readBody(r, &body)
	h.mu.Lock()
	defer h.mu.Unlock()
	idx := -1
	for i, v := range h.versions {
		if v.Path == body.P && v.File == body.ID {
			idx = i
		}
	}
	if idx < 0 {
		writeErr(w, http.StatusNotFound, T("versione non trovata"))
		return
	}
	if err := h.restoreVersionLocked(idx, time.Now().UnixNano()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := h.saveIndex(); err != nil {
		h.log.Printf(T("ERRORE salvataggio indice: %v"), err)
	}
	h.broadcast()
	h.log.Print(T("Ripristinata una versione precedente di un file"))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
