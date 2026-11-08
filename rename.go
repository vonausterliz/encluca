package main

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"encluca/internal/rcrypt"
)

// Nomi troppo lunghi per essere cifrati: la scansione li segnala con un nome
// alternativo suggerito, che l'utente può accettare o modificare dall'interfaccia.

// segmentFitsLocked dice se un singolo nome (file o cartella) resta entro il limite una volta cifrato.
func (c *Client) segmentFitsLocked(name string) bool {
	return len(c.cip.EncryptSegment(name)) <= rcrypt.MaxSegment
}

// noteLongNameLocked individua l'elemento del percorso troppo lungo (il file o una sua cartella)
// e prepara un nome suggerito. Più file nella stessa cartella lunga producono un solo suggerimento.
func (c *Client) noteLongNameLocked(rel string) {
	parts := strings.Split(rel, "/")
	for i, p := range parts {
		if c.segmentFitsLocked(p) {
			continue
		}
		comp := strings.Join(parts[:i+1], "/")
		if _, ok := c.renames[comp]; !ok {
			dirRel := path.Dir(comp)
			c.renames[comp] = suggestName(p, c.segmentFitsLocked, func(n string) bool {
				return c.nameTakenLocked(dirRel, n, comp)
			})
		}
		return
	}
}

// suggestName propone un nome più corto che rispetti fits e non esista già.
// Se il nome è composto da parti separate (es. "Titolo -- Autore -- Editore -- isbn"),
// tiene le prime parti; altrimenti taglia all'ultima parola intera. L'estensione resta.
func suggestName(name string, fits func(string) bool, exists func(string) bool) string {
	ext := path.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	if ext == "" || len(ext) > 10 || stem == "" {
		stem, ext = name, ""
	}
	tidy := func(s string) string { return strings.TrimRight(s, " -_.,;—–") }

	var base string
	for _, sep := range []string{" -- ", " — ", " – ", " - "} {
		parts := strings.Split(stem, sep)
		if len(parts) < 2 {
			continue
		}
		for k := len(parts) - 1; k >= 1; k-- {
			if cand := tidy(strings.Join(parts[:k], sep)); cand != "" && fits(cand+ext) {
				base = cand
				break
			}
		}
		if base != "" {
			break
		}
	}
	if base == "" {
		base = truncateToFit(stem, ext, fits)
	}

	cand := base + ext
	for i := 2; exists(cand) || !fits(cand); i++ {
		suffix := fmt.Sprintf(" (%d)", i)
		cand = truncateToFit(base, suffix+ext, fits) + suffix + ext
		if i > 999 {
			break
		}
	}
	return cand
}

// truncateToFit accorcia stem (senza spezzare caratteri UTF-8, preferibilmente a fine parola)
// finché stem+tail rispetta fits.
func truncateToFit(stem, tail string, fits func(string) bool) string {
	s := stem
	for s != "" && !fits(s+tail) {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	if s != stem {
		if i := strings.LastIndexByte(s, ' '); i > len(s)/2 {
			s = s[:i]
		}
	}
	s = strings.TrimRight(s, " -_.,;—–")
	if s == "" {
		s = "file"
	}
	return s
}

// Rename rinomina un elemento segnalato come troppo lungo. Accetta solo elementi
// presenti nell'elenco dei suggerimenti e un nome semplice (nessun percorso).
func (c *Client) Rename(comp, name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cip == nil {
		return errors.New("apri la cassaforte per rinominare")
	}
	if _, ok := c.renames[comp]; !ok {
		return errors.New("questo elemento non è più da rinominare")
	}
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) || strings.ContainsRune(name, 0) {
		return errors.New("nome non valido: niente barre e non vuoto")
	}
	if !c.segmentFitsLocked(name) {
		return errors.New("anche questo nome è troppo lungo: accorcialo ancora")
	}
	if c.nameTakenLocked(path.Dir(comp), name, comp) {
		return fmt.Errorf("esiste già un elemento chiamato %q nella cassaforte: scegli un altro nome", name)
	}
	from := filepath.Join(c.cfg.WorkDir, filepath.FromSlash(comp))
	to := filepath.Join(filepath.Dir(from), name)
	if err := os.Rename(from, to); err != nil {
		return err
	}
	c.log.Printf("Rinominato: %s → %s", path.Base(comp), name)
	if changed, _ := c.scanLocked(false); changed {
		c.saveState()
		go c.kick()
	}
	return nil
}

// longNameCoveredLocked dice se il problema di rel è già rappresentato da un suggerimento di rinomina.
func (c *Client) longNameCoveredLocked(rel string) bool {
	for comp := range c.renames {
		if rel == comp || strings.HasPrefix(rel, comp+"/") {
			return true
		}
	}
	return false
}

// nameTakenLocked dice se name è già occupato nella cartella dirRel ("." = radice), confrontando
// senza distinguere maiuscole e minuscole (su macOS e Windows "A.txt" e "a.txt" sono lo stesso file):
//   - nella cartella aperta;
//   - nella cassaforte, anche per file non presenti in chiaro (in arrivo, in coda, ecc.);
//   - tra i nomi già suggeriti per altri elementi della stessa cartella.
//
// self è l'elemento da rinominare, escluso dal confronto con i suggerimenti.
func (c *Client) nameTakenLocked(dirRel, name, self string) bool {
	if dirRel == "." {
		dirRel = ""
	}
	if ents, err := os.ReadDir(filepath.Join(c.cfg.WorkDir, filepath.FromSlash(dirRel))); err == nil {
		for _, e := range ents {
			if strings.EqualFold(e.Name(), name) {
				return true
			}
		}
	}
	for comp, sug := range c.renames {
		if comp != self && strings.EqualFold(sug, name) && path.Dir(comp) == path.Clean("./"+dirRel) {
			return true
		}
	}
	// indice della cassaforte: si confrontano i nomi in chiaro degli elementi di quella cartella
	prefix := ""
	if dirRel != "" {
		enc, err := c.encPathLocked(dirRel)
		if err != nil {
			return false
		}
		prefix = enc + "/"
	}
	for enc, st := range c.st.Files {
		if st.Del || !strings.HasPrefix(enc, prefix) {
			continue
		}
		first, _, _ := strings.Cut(strings.TrimPrefix(enc, prefix), "/")
		plain, err := c.cip.DecryptSegment(first)
		if err == nil && strings.EqualFold(plain, name) {
			return true
		}
	}
	return false
}
