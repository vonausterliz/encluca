package main

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

// Controllo dei file in uso: prima di chiudere la cassaforte (a mano o in automatico) si
// verifica che nessun programma tenga aperto un file della cartella aperta. Su Linux e macOS
// il sistema permette di cancellare un file aperto: il programma continuerebbe a mostrarlo
// e al salvataggio le modifiche andrebbero perse o finirebbero in chiaro fuori dalla cassaforte.

// inUseFile è un file della cartella aperta in uso da un programma.
type inUseFile struct {
	Path string `json:"path"`           // relativo alla cartella aperta, separatore "/"
	Proc string `json:"proc,omitempty"` // nome del programma, se il sistema lo dice
	Lock bool   `json:"lock,omitempty"` // rilevato da un file di blocco del programma
}

// inUseError blocca la chiusura finché i file non vengono chiusi.
type inUseError struct{ Files []inUseFile }

func (e *inUseError) Error() string {
	names := make([]string, 0, 4)
	for i, f := range e.Files {
		if i == 3 {
			names = append(names, fmt.Sprintf(T("e altri %d"), len(e.Files)-3))
			break
		}
		names = append(names, f.Path)
	}
	return fmt.Sprintf(T("non chiudo: %d file sono aperti in un programma (%s). Chiudili e riprova"), len(e.Files), strings.Join(names, "; "))
}

// indicizzatori e simili: aprono i file per un attimo e non devono bloccare la chiusura
var ignoredProcs = []string{
	"tracker-miner-fs-3", "tracker-extract-3", "localsearch-3", "localsearch-extractor-3",
	"baloo_file", "baloo_file_extractor", "mdworker", "mdworker_shared", "mds", "mds_stores",
	"encluca", "nautilus",
}

// ignoredProc: su Linux il nome del processo (/proc/PID/comm) è troncato a 15 caratteri.
func ignoredProc(name string) bool {
	for _, n := range ignoredProcs {
		if name == n || len(name) == 15 && strings.HasPrefix(n, name) {
			return true
		}
	}
	return false
}

// lockFiles trova i file di blocco che i programmi creano accanto al documento aperto.
func lockFiles(work string) []inUseFile {
	var out []inUseFile
	filepath.WalkDir(work, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		name, doc := d.Name(), ""
		switch {
		case strings.HasPrefix(name, ".~lock.") && strings.HasSuffix(name, "#") && len(name) > 8: // LibreOffice
			doc = name[len(".~lock.") : len(name)-1]
		case strings.HasPrefix(name, "~$"): // Word, Excel: il nome del documento è troncato
			doc = name
		case strings.HasPrefix(name, ".") && strings.HasSuffix(name, ".swp") && len(name) > 5: // vim
			doc = name[1 : len(name)-len(".swp")]
		default:
			return nil
		}
		if rel, err := filepath.Rel(work, filepath.Join(filepath.Dir(p), doc)); err == nil {
			out = append(out, inUseFile{Path: filepath.ToSlash(rel), Lock: true})
		}
		return nil
	})
	return out
}

// filesInUse unisce i file aperti dai programmi e quelli segnalati dai file di blocco.
func filesInUse(work string) ([]inUseFile, error) {
	if real, err := filepath.EvalSymlinks(work); err == nil {
		work = real
	}
	open, err := openByProcesses(work)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []inUseFile
	for _, f := range append(open, lockFiles(work)...) {
		if !seen[f.Path] {
			seen[f.Path] = true
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// relInWork restituisce il percorso relativo se abs è un file regolare dentro work.
func relInWork(work, abs string) (string, bool) {
	if !strings.HasPrefix(abs, work+string(filepath.Separator)) {
		return "", false
	}
	rel, err := filepath.Rel(work, abs)
	if err != nil {
		return "", false
	}
	return filepath.ToSlash(rel), true
}
