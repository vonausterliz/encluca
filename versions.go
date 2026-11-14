package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
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
	h.pruneVersionsLocked()
	h.saveVersionsLocked()
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
