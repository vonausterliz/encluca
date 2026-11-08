package main

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"

	"encluca/internal/rcrypt"
)

// Esplorazione della cassaforte dalla pagina del NAS (funzione facoltativa, spenta di default).
// L'hub fornisce solo dati cifrati: nomi e contenuti vengono decifrati nel browser dal modulo
// WebAssembly (cmd/vaultwasm), con la password inserita nella pagina. La password non arriva mai qui.

func (h *Hub) browseEnabled() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cfg.BrowseEnabled
}

// withBrowse aggiunge a withAdmin il controllo che la funzione sia attiva.
func (h *Hub) withBrowse(fn http.HandlerFunc) http.HandlerFunc {
	return h.withAdmin(func(w http.ResponseWriter, r *http.Request) {
		if !h.browseEnabled() {
			writeErr(w, http.StatusForbidden, T("funzione disattivata: attivala in Impostazioni"))
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		fn(w, r)
	})
}

func (h *Hub) adminBrowseToggle(w http.ResponseWriter, r *http.Request) {
	var body struct{ Enabled bool }
	readBody(r, &body)
	h.mu.Lock()
	h.cfg.BrowseEnabled = body.Enabled
	err := h.saveCfg()
	h.mu.Unlock()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if body.Enabled {
		h.log.Printf(T("Esplorazione della cassaforte dal NAS attivata"))
	} else {
		h.log.Printf(T("Esplorazione della cassaforte dal NAS disattivata"))
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// metadati della cassaforte (salt e valore di controllo): servono al browser per verificare la password
func (h *Hub) apiBrowseMeta(w http.ResponseWriter, r *http.Request) {
	b, err := os.ReadFile(h.metaPath())
	if err != nil {
		writeErr(w, http.StatusNotFound, T("cassaforte non ancora creata"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}

type browseItem struct {
	P     string `json:"p"`     // percorso cifrato
	Size  int64  `json:"size"`  // dimensione in chiaro
	MTime int64  `json:"mtime"` // ns
}

func (h *Hub) apiBrowseList(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	items := make([]browseItem, 0, len(h.idx.Entries))
	for p, e := range h.idx.Entries {
		if !e.Del {
			items = append(items, browseItem{p, rcrypt.DecryptedSize(e.Size), e.MTime})
		}
	}
	h.mu.Unlock()
	sort.Slice(items, func(i, j int) bool { return items[i].P < items[j].P })
	writeJSON(w, http.StatusOK, items)
}

func (h *Hub) apiBrowseFile(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("p")
	if !validEncPath(p) {
		writeErr(w, http.StatusBadRequest, T("percorso non valido"))
		return
	}
	f, err := os.Open(filepath.Join(h.vaultDir(), filepath.FromSlash(p)))
	if err != nil {
		writeErr(w, http.StatusNotFound, T("non trovato"))
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, "", fileModTime(f), f)
}

// file statici del modulo di decifratura (codice pubblico, nessun dato)
func serveStatic(name, ctype string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := webFS.ReadFile(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", ctype)
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(b)
	}
}
