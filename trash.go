package main

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Cestino dell'hub: un file cancellato da un dispositivo non viene distrutto subito ma spostato
// (cifrato) in trash/ e conservato per trashRetention. Così un errore o un ransomware che
// svuota la cartella su un PC non fa perdere i dati: si ripristinano dalla pagina del NAS.

const trashRetention = 30 * 24 * time.Hour

type trashItem struct {
	Path    string `json:"p"`
	File    string `json:"file"` // nome del file in trash/
	Size    int64  `json:"size"`
	SHA     string `json:"sha"`
	MTime   int64  `json:"mtime"`
	Deleted int64  `json:"deleted"` // unix nano
	Dev     string `json:"dev,omitempty"`
	Dir     bool   `json:"dir,omitempty"` // cartella vuota: nessun file in trash/
}

func (h *Hub) trashDir() string       { return filepath.Join(h.dir, "trash") }
func (h *Hub) trashIndexPath() string { return filepath.Join(h.dir, "trash.json") }

func (h *Hub) loadTrash() {
	if err := loadJSON(h.trashIndexPath(), &h.trash); err != nil && !errors.Is(err, fs.ErrNotExist) {
		h.log.Printf(T("ERRORE lettura cestino: %v"), err)
	}
	os.MkdirAll(h.trashDir(), 0o700)
	h.mu.Lock()
	h.purgeTrashLocked()
	h.mu.Unlock()
}

func (h *Hub) saveTrashLocked() {
	if err := saveJSON(h.trashIndexPath(), &h.trash); err != nil {
		h.log.Printf(T("ERRORE salvataggio cestino: %v"), err)
	}
}

// trashFileLocked sposta nel cestino il file cifrato di e (che sta per essere cancellato).
func (h *Hub) trashFileLocked(full string, e *Entry, dev string) error {
	name := randHex(16)
	if err := os.Rename(full, filepath.Join(h.trashDir(), name)); err != nil {
		return err
	}
	h.trash = append(h.trash, trashItem{Path: e.Path, File: name, Size: e.Size, SHA: e.SHA, MTime: e.MTime, Deleted: time.Now().UnixNano(), Dev: dev})
	h.purgeTrashLocked()
	h.saveTrashLocked()
	return nil
}

// trashDirLocked registra nel cestino una cartella vuota cancellata (non c'è nessun file da spostare).
func (h *Hub) trashDirLocked(e *Entry, dev string) {
	h.trash = append(h.trash, trashItem{Path: e.Path, MTime: e.MTime, Deleted: time.Now().UnixNano(), Dev: dev, Dir: true})
	h.purgeTrashLocked()
	h.saveTrashLocked()
}

// purgeTrashLocked elimina definitivamente gli elementi più vecchi di trashRetention.
func (h *Hub) purgeTrashLocked() {
	limit := time.Now().Add(-trashRetention).UnixNano()
	out := h.trash[:0]
	purged := false
	for _, it := range h.trash {
		if it.Deleted < limit {
			if !it.Dir {
				os.Remove(filepath.Join(h.trashDir(), it.File))
			}
			purged = true
			continue
		}
		out = append(out, it)
	}
	h.trash = out
	if purged {
		h.saveTrashLocked()
	}
}

func (h *Hub) trashStatsLocked() map[string]any {
	var size int64
	var oldest int64
	for _, it := range h.trash {
		size += it.Size
		if oldest == 0 || it.Deleted < oldest {
			oldest = it.Deleted
		}
	}
	res := map[string]any{"files": len(h.trash), "bytes": size}
	if oldest > 0 {
		res["oldest"] = time.Unix(0, oldest)
	}
	return res
}

// restoreTrashLocked rimette nella cassaforte i file del cestino cancellati dopo since
// (0 = tutti) e ancora assenti: per ogni percorso vale la versione cancellata più di recente.
// I dispositivi li ricevono come normali modifiche. Restituisce quanti file sono tornati.
func (h *Hub) restoreTrashLocked(since int64) int {
	idx := make([]int, 0, len(h.trash))
	for i := range h.trash {
		idx = append(idx, i)
	}
	sort.SliceStable(idx, func(a, b int) bool { return h.trash[idx[a]].Deleted > h.trash[idx[b]].Deleted })
	done := map[string]bool{}
	var restored []int
	for _, i := range idx {
		it := h.trash[i]
		if it.Deleted < since || done[it.Path] {
			continue
		}
		done[it.Path] = true
		cur := h.idx.Entries[it.Path]
		if cur != nil && !cur.Del {
			continue // nel frattempo il file è stato ricreato: la versione attuale prevale
		}
		full := filepath.Join(h.vaultDir(), filepath.FromSlash(it.Path))
		if it.Dir {
			if os.MkdirAll(full, 0o700) != nil {
				continue
			}
		} else if os.MkdirAll(filepath.Dir(full), 0o700) != nil || os.Rename(filepath.Join(h.trashDir(), it.File), full) != nil {
			continue
		}
		if it.MTime > 0 {
			t := time.Unix(0, it.MTime)
			os.Chtimes(full, t, t)
		}
		var rev int64
		if cur != nil {
			rev = cur.Rev
		}
		h.idx.Seq++
		h.idx.Entries[it.Path] = &Entry{Path: it.Path, Rev: rev + 1, Size: it.Size, SHA: it.SHA, MTime: it.MTime, Seq: h.idx.Seq, Dev: "trash", Dir: it.Dir}
		restored = append(restored, i)
	}
	if len(restored) == 0 {
		return 0
	}
	drop := map[int]bool{}
	for _, i := range restored {
		drop[i] = true
	}
	out := h.trash[:0]
	for i, it := range h.trash {
		if !drop[i] {
			out = append(out, it)
		}
	}
	h.trash = out
	h.saveTrashLocked()
	if err := h.saveIndex(); err != nil {
		h.log.Printf(T("ERRORE salvataggio indice: %v"), err)
	}
	h.broadcast()
	return len(restored)
}

// adminTrashRestore: POST {Hours: n} ripristina i file cancellati nelle ultime n ore (0 = tutti).
func (h *Hub) adminTrashRestore(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, T("metodo non ammesso"))
		return
	}
	var body struct{ Hours int }
	readBody(r, &body)
	var since int64
	if body.Hours > 0 {
		since = time.Now().Add(-time.Duration(body.Hours) * time.Hour).UnixNano()
	}
	h.mu.Lock()
	n := h.restoreTrashLocked(since)
	h.mu.Unlock()
	h.log.Printf(T("Ripristinati dal cestino: %d file"), n)
	writeJSON(w, http.StatusOK, map[string]int{"restored": n})
}
