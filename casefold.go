package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Su macOS e Windows il disco di solito non distingue maiuscole e minuscole: "Foto.jpg" e
// "foto.jpg" sono lo stesso file. Su Linux invece sono due file diversi, e la cassaforte li
// tiene entrambi. Se arrivassero tutti e due su un Mac, il secondo sovrascriverebbe il primo
// e la scansione manderebbe al NAS il contenuto sbagliato e la cancellazione dell'altro.
//
// Quindi su questi dischi una voce il cui nome coincide (a parte le maiuscole) con un elemento
// già presente NON viene scritta nella cartella aperta: resta cifrata, al sicuro nella
// cassaforte, e l'interfaccia lo segnala. Compare da sola appena uno dei due viene rinominato.

var errCaseClash = errors.New("nome uguale a un altro a parte le maiuscole")

// caseFoldProbe dice se la cartella dir sta su un disco che non distingue le maiuscole.
var caseFoldProbe = func(dir string) bool {
	upper := filepath.Join(dir, tmpPrefix+"CaseProbe")
	if err := os.WriteFile(upper, nil, 0o600); err != nil {
		return false
	}
	defer os.Remove(upper)
	_, err := os.Stat(filepath.Join(dir, tmpPrefix+"caseprobe"))
	return err == nil
}

// caseClashLocked registra rel come nome della voce enc nella cartella aperta. Restituisce
// true se su questo disco coinciderebbe con un altro elemento presente: in quel caso la voce
// non va scritta.
func (c *Client) caseClashLocked(enc, rel string) bool {
	if !c.caseFold {
		return false
	}
	key := strings.ToLower(rel)
	if other, ok := c.fold[key]; ok && other != enc {
		if st := c.st.Files[other]; st != nil && !st.Del && st.Plain != nil {
			if _, known := c.clash[enc]; !known {
				c.log.Printf(T("ATTENZIONE: %s ha lo stesso nome di un altro elemento a parte le maiuscole: su questo computer non è visibile, ma resta al sicuro nella cassaforte"), rel)
			}
			c.clash[enc] = rel
			return true
		}
	}
	c.fold[key] = enc
	delete(c.clash, enc)
	return false
}

// noteLocalNameLocked registra un elemento trovato dalla scansione nella cartella aperta.
func (c *Client) noteLocalNameLocked(enc, rel string) {
	if c.caseFold {
		c.fold[strings.ToLower(rel)] = enc
		delete(c.clash, enc)
	}
}

// retryClashesLocked prova a mostrare le voci rimaste nascoste: riesce quando l'elemento con
// cui coincidevano è stato rinominato o eliminato.
func (c *Client) retryClashesLocked() {
	for enc, rel := range c.clash {
		st := c.st.Files[enc]
		if st == nil || st.Del {
			delete(c.clash, enc)
			continue
		}
		if st.Plain != nil {
			continue
		}
		if st.Dir {
			if !c.caseClashLocked(enc, rel) && os.MkdirAll(filepath.Join(c.cfg.WorkDir, filepath.FromSlash(rel)), 0o700) == nil {
				st.Plain = &plainSnap{}
			}
			continue
		}
		if err := c.decryptToWorkLocked(enc, st); err == nil {
			c.log.Printf(T("Ora visibile: %s"), rel)
		}
	}
}
