package main

import (
	"errors"
	"path/filepath"
	"runtime"
	"strings"
)

// I nomi dei file arrivano dal NAS cifrati e vengono decifrati qui. Chi conosce la password
// della cassaforte (per esempio un altro PC compromesso) può cifrare un nome qualunque, anche
// "../../.bashrc": prima di toccare il disco ogni nome decifrato deve restare dentro la
// cartella aperta. Senza questo controllo un PC infetto potrebbe leggere, scrivere e
// cancellare file fuori dalla cassaforte su tutti gli altri PC.

var errUnsafeName = errors.New("nome non ammesso")

// safeRelFor dice se rel (percorso in chiaro con "/") è un percorso relativo che resta dentro
// la cartella aperta sul sistema goos.
func safeRelFor(goos, rel string) bool {
	if rel == "" || strings.ContainsRune(rel, 0) {
		return false
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
		// su Windows "\" separa le cartelle e ":" indica un'unità o un flusso nascosto
		if goos == "windows" && strings.ContainsAny(seg, `\:`) {
			return false
		}
	}
	return true
}

// plainRelLocked decifra un percorso ricevuto dal NAS e lo restituisce solo se è sicuro.
func (c *Client) plainRelLocked(enc string) (string, error) {
	rel, err := c.cip.DecryptPath(enc)
	if err != nil {
		return "", err
	}
	// filepath.IsLocal aggiunge le regole del sistema in uso (su Windows anche i nomi
	// riservati come NUL o COM1)
	if !safeRelFor(runtime.GOOS, rel) || !filepath.IsLocal(filepath.FromSlash(rel)) {
		return "", errUnsafeName
	}
	return rel, nil
}
