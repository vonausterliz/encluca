// Strumento di firma dei rilasci di Encluca.
//
//	go run ./cmd/sign keygen [PATH]   genera la chiave (una volta sola)
//	go run ./cmd/sign sign FILE       scrive FILE.sig (ed25519, base64)
//
// La chiave privata resta fuori dal repo (predefinito
// ~/.config/encluca-release/signing.key, permessi 0600); la variabile
// ENCLUCA_SIGNING_KEY ne cambia il percorso.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func defaultKeyPath() string {
	if p := os.Getenv("ENCLUCA_SIGNING_KEY"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "encluca-release", "signing.key")
}

// keygen crea una nuova chiave in path (rifiuta di sovrascriverne una
// esistente) e restituisce la chiave pubblica in hex.
func keygen(path string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("la chiave esiste già in %s: non la sovrascrivo", path)
		}
		return "", err
	}
	if _, err := f.WriteString(hex.EncodeToString(priv) + "\n"); err != nil {
		f.Close()
		os.Remove(path)
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return hex.EncodeToString(pub), nil
}

func loadKey(path string) (ed25519.PrivateKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	b, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(b) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("chiave non valida in %s", path)
	}
	return ed25519.PrivateKey(b), nil
}

// signFile scrive file+".sig" con la firma ed25519 del contenuto di file.
func signFile(keyPath, file string) error {
	priv, err := loadKey(keyPath)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	sig := ed25519.Sign(priv, data)
	return os.WriteFile(file+".sig", []byte(base64.StdEncoding.EncodeToString(sig)+"\n"), 0o644)
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "uso: sign keygen [PATH] | sign sign FILE")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "keygen":
		path := defaultKeyPath()
		if len(os.Args) > 2 {
			path = os.Args[2]
		}
		pub, err := keygen(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "errore:", err)
			os.Exit(1)
		}
		fmt.Println("Chiave privata creata:", path)
		fmt.Println("Chiave pubblica (da mettere in update_key.go):", pub)
		fmt.Println("IMPORTANTE: fai subito una copia di backup della chiave privata in un posto sicuro.")
		fmt.Println("Se la perdi, i client installati non accetteranno più aggiornamenti automatici.")
	case "sign":
		if len(os.Args) != 3 {
			fmt.Fprintln(os.Stderr, "uso: sign sign FILE")
			os.Exit(2)
		}
		if err := signFile(defaultKeyPath(), os.Args[2]); err != nil {
			fmt.Fprintln(os.Stderr, "errore:", err)
			os.Exit(1)
		}
		fmt.Println("Firmato:", os.Args[2]+".sig")
	default:
		fmt.Fprintln(os.Stderr, "comando sconosciuto:", os.Args[1])
		os.Exit(2)
	}
}
