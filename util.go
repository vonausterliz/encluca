package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// writeFileAtomic scrive su file temporaneo e poi rinomina, così un crash non lascia file a metà.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	tmp.Close()
	os.Chmod(name, perm)
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

func saveJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, b, 0o600)
}

func loadJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func randPassword(n int) string {
	const alphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, n)
	io.ReadFull(rand.Reader, b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// ringLog tiene le ultime righe di log per mostrarle nell'interfaccia.
type ringLog struct {
	mu    sync.Mutex
	lines []string
	max   int
	out   *log.Logger
}

func newRingLog(file string, max int) *ringLog {
	var w io.Writer = os.Stderr
	if file != "" {
		os.MkdirAll(filepath.Dir(file), 0o700)
		if f, err := os.OpenFile(file, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
			if runtime.GOOS == "windows" {
				w = f // su Windows il servizio non ha console
			} else {
				w = io.MultiWriter(os.Stderr, f)
			}
		}
	}
	return &ringLog{max: max, out: log.New(w, "", log.LstdFlags)}
}

func (r *ringLog) Printf(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	r.out.Print(msg)
	r.mu.Lock()
	r.lines = append(r.lines, time.Now().Format("15:04:05")+"  "+msg)
	if len(r.lines) > r.max {
		r.lines = r.lines[len(r.lines)-r.max:]
	}
	r.mu.Unlock()
}

func (r *ringLog) Lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.lines))
	for i := range r.lines {
		out[i] = r.lines[len(r.lines)-1-i]
	}
	return out
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// validEncPath accetta solo percorsi cifrati (base32hex minuscolo separato da "/"):
// impedisce qualunque path traversal sull'hub.
func validEncPath(p string) bool {
	if p == "" || len(p) > 4096 {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || len(seg) > 255 {
			return false
		}
		for _, ch := range seg {
			if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'v')) {
				return false
			}
		}
	}
	return true
}

// defaultClientDir restituisce la cartella dati del client per il sistema operativo corrente.
func defaultClientDir() string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "windows":
		if d := os.Getenv("LOCALAPPDATA"); d != "" {
			return filepath.Join(d, "Encluca")
		}
		return filepath.Join(home, "AppData", "Local", "Encluca")
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Encluca")
	default:
		if d := os.Getenv("XDG_DATA_HOME"); d != "" {
			return filepath.Join(d, "encluca")
		}
		return filepath.Join(home, ".local", "share", "encluca")
	}
}

func defaultWorkDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Encluca - APERTA")
}

// markFolder rende la cartella aperta riconoscibile anche nel file manager (icona su KDE/Dolphin).
func markFolder(dir string) {
	if runtime.GOOS == "linux" {
		os.WriteFile(filepath.Join(dir, ".directory"), []byte("[Desktop Entry]\nIcon=folder-important\nComment=Encluca: cartella DECRIPTATA\n"), 0o600)
	}
}

// progress descrive l'avanzamento di un'operazione su più file (invio, ricezione, backup).
type progress struct {
	Done       int   `json:"done"`
	Total      int   `json:"total"`
	DoneBytes  int64 `json:"doneBytes"`
	TotalBytes int64 `json:"totalBytes"`
}

// snapshot restituisce una copia (da serializzare fuori dal lock), o nil.
func (p *progress) snapshot() *progress {
	if p == nil {
		return nil
	}
	c := *p
	return &c
}
