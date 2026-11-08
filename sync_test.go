package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type testEnv struct {
	t      *testing.T
	hub    *Hub
	srv    *httptest.Server
	down   atomic.Bool
	ctx    context.Context
	cancel context.CancelFunc
}

func newTestEnv(t *testing.T) *testEnv {
	env := &testEnv{t: t}
	h, err := newHub(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	env.hub = h
	inner := h.handler()
	// "spegne" il NAS chiudendo le connessioni, come se non fosse raggiungibile
	env.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if env.down.Load() {
			if hj, ok := w.(http.Hijacker); ok {
				conn, _, _ := hj.Hijack()
				conn.Close()
				return
			}
		}
		inner.ServeHTTP(w, r)
	}))
	env.srv.TLS = &tls.Config{Certificates: []tls.Certificate{h.cert}}
	env.srv.StartTLS()
	env.ctx, env.cancel = context.WithCancel(context.Background())
	t.Cleanup(func() { env.cancel(); env.srv.Close() })
	return env
}

func (env *testEnv) pairCode(name string) string {
	tok := randHex(32)
	sum := sha256.Sum256([]byte(tok))
	env.hub.mu.Lock()
	env.hub.cfg.Devices = append(env.hub.cfg.Devices, &Device{ID: randHex(6), Name: name, TokenHash: hex.EncodeToString(sum[:])})
	env.hub.mu.Unlock()
	b, _ := json.Marshal(map[string]string{"u": env.srv.URL, "t": tok, "f": env.hub.fingerprint})
	return "EL1-" + base64.RawURLEncoding.EncodeToString(b)
}

func (env *testEnv) newClient(name string) *Client {
	dir := env.t.TempDir()
	c, err := newClient(filepath.Join(dir, "data"))
	if err != nil {
		env.t.Fatal(err)
	}
	c.cfg.WorkDir = filepath.Join(dir, "Encluca - APERTA")
	if err := c.Pair(env.pairCode(name), "", name); err != nil {
		env.t.Fatal(err)
	}
	c.run(env.ctx)
	return c
}

func (env *testEnv) hubFiles() int {
	env.hub.mu.Lock()
	defer env.hub.mu.Unlock()
	return len(env.hub.idx.Entries)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timeout in attesa di: %s", what)
}

func readWork(c *Client, rel string) string {
	b, err := os.ReadFile(filepath.Join(c.cfg.WorkDir, filepath.FromSlash(rel)))
	if err != nil {
		return "<assente>"
	}
	return string(b)
}

func writeWork(t *testing.T, c *Client, rel, content string) {
	t.Helper()
	p := filepath.Join(c.cfg.WorkDir, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(p), 0o700)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func pendingCount(c *Client) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, st := range c.st.Files {
		if st.Pending != "" || st.Conflict > 0 {
			n++
		}
	}
	return n
}

func listWork(c *Client) []string {
	var out []string
	filepath.WalkDir(c.cfg.WorkDir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && !ignored(d.Name()) {
			rel, _ := filepath.Rel(c.cfg.WorkDir, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	return out
}

func TestEndToEnd(t *testing.T) {
	env := newTestEnv(t)
	const pw = "una password lunga e sicura"

	// 1. il primo PC crea la cassaforte e aggiunge un file
	pc1 := env.newClient("PC1")
	if err := pc1.CreateVault(pw); err != nil {
		t.Fatal(err)
	}
	writeWork(t, pc1, "Documenti/Tasse/730 segreto.txt", "reddito: 12345 euro")
	waitFor(t, "upload da PC1", func() bool { return pendingCount(pc1) == 0 && env.hubFiles() == 1 })

	// 2. il secondo PC si collega, apre e vede il file
	pc2 := env.newClient("PC2")
	if err := pc2.Open("password sbagliata!!"); err == nil {
		t.Fatal("aperta con password errata")
	}
	if err := pc2.Open(pw); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "file su PC2", func() bool { return readWork(pc2, "Documenti/Tasse/730 segreto.txt") == "reddito: 12345 euro" })

	// 3. modifica su PC2 -> arriva su PC1
	writeWork(t, pc2, "Documenti/Tasse/730 segreto.txt", "reddito: 99999 euro")
	waitFor(t, "modifica su PC1", func() bool { return readWork(pc1, "Documenti/Tasse/730 segreto.txt") == "reddito: 99999 euro" })

	// 4. NAS spento: PC1 modifica, chiude (resta tutto cifrato in coda); poi il NAS torna
	env.down.Store(true)
	writeWork(t, pc1, "Offline.txt", "scritto senza NAS")
	waitFor(t, "modifica in coda su PC1", func() bool { return pendingCount(pc1) == 1 })
	if err := pc1.Close(); err != nil {
		t.Fatalf("chiusura offline: %v", err)
	}
	if _, err := os.Stat(pc1.cfg.WorkDir); !os.IsNotExist(err) {
		t.Fatal("la cartella in chiaro esiste ancora dopo la chiusura")
	}
	env.down.Store(false)
	waitFor(t, "invio dopo ritorno del NAS", func() bool { return pendingCount(pc1) == 0 })
	waitFor(t, "file offline su PC2", func() bool { return readWork(pc2, "Offline.txt") == "scritto senza NAS" })

	// 5. conflitto: entrambi modificano lo stesso file mentre il NAS è spento
	if err := pc1.Open(pw); err != nil {
		t.Fatal(err)
	}
	if readWork(pc1, "Offline.txt") != "scritto senza NAS" {
		t.Fatal("riapertura: contenuto errato")
	}
	env.down.Store(true)
	writeWork(t, pc1, "Offline.txt", "versione PC1")
	writeWork(t, pc2, "Offline.txt", "versione PC2")
	waitFor(t, "modifiche in coda", func() bool { return pendingCount(pc1) == 1 && pendingCount(pc2) == 1 })
	env.down.Store(false)
	waitFor(t, "risoluzione conflitto", func() bool {
		return pendingCount(pc1) == 0 && pendingCount(pc2) == 0 && len(listWork(pc1)) == 3 && len(listWork(pc2)) == 3
	})
	var contents []string
	for _, rel := range listWork(pc1) {
		if strings.HasPrefix(rel, "Offline") {
			contents = append(contents, readWork(pc1, rel))
			if readWork(pc2, rel) != readWork(pc1, rel) {
				t.Fatalf("PC1 e PC2 non allineati su %s", rel)
			}
		}
	}
	joined := strings.Join(contents, "|")
	if !strings.Contains(joined, "versione PC1") || !strings.Contains(joined, "versione PC2") {
		t.Fatalf("una delle due versioni è andata persa: %v (file: %v)", contents, listWork(pc1))
	}
	t.Logf("dopo il conflitto: %v", listWork(pc1))

	// 6. cancellazione propagata
	os.Remove(filepath.Join(pc2.cfg.WorkDir, "Documenti", "Tasse", "730 segreto.txt"))
	waitFor(t, "cancellazione su PC1", func() bool { return readWork(pc1, "Documenti/Tasse/730 segreto.txt") == "<assente>" })

	// 7. sul NAS non c'è nulla in chiaro (né contenuti né nomi)
	filepath.WalkDir(env.hub.dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, _ := os.ReadFile(p)
		for _, secret := range []string{"versione PC", "reddito", "scritto senza NAS", "Tasse", "Offline", "Documenti"} {
			if bytes.Contains(b, []byte(secret)) {
				t.Errorf("testo in chiaro %q trovato sul NAS in %s", secret, p)
			}
		}
		if strings.Contains(p, "Tasse") || strings.Contains(p, "Offline") {
			t.Errorf("nome in chiaro sul NAS: %s", p)
		}
		return nil
	})

	if err := pc1.Close(); err != nil {
		t.Fatal(err)
	}
	if err := pc2.Close(); err != nil {
		t.Fatal(err)
	}
}

// Un file che non può essere cifrato (es. nome troppo lungo) esiste solo in chiaro:
// la chiusura deve rifiutarsi, altrimenti cancellerebbe l'unica copia.
func TestCloseKeepsUnencryptableFiles(t *testing.T) {
	env := newTestEnv(t)
	pc := env.newClient("PC1")
	if err := pc.CreateVault("una password lunga e sicura"); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("Il cavaliere inesistente ", 8) + "– Anna’s Archive.epub"
	writeWork(t, pc, long, "contenuto unico")
	waitFor(t, "segnalazione del nome lungo", func() bool {
		pc.mu.Lock()
		defer pc.mu.Unlock()
		return pc.problems[long] != ""
	})
	if err := pc.Close(); err == nil {
		t.Fatal("chiusa con un file non cifrabile: sarebbe andato perso")
	}
	if readWork(pc, long) != "contenuto unico" {
		t.Fatal("il file non cifrabile è stato cancellato")
	}
	// dopo averlo rinominato la chiusura riesce e il file è al sicuro nella cassaforte
	if err := os.Rename(filepath.Join(pc.cfg.WorkDir, long), filepath.Join(pc.cfg.WorkDir, "Il cavaliere inesistente.epub")); err != nil {
		t.Fatal(err)
	}
	if err := pc.Close(); err != nil {
		t.Fatalf("chiusura dopo la rinomina: %v", err)
	}
	if err := pc.Open("una password lunga e sicura"); err != nil {
		t.Fatal(err)
	}
	if readWork(pc, "Il cavaliere inesistente.epub") != "contenuto unico" {
		t.Fatal("file rinominato non ritrovato dopo la riapertura")
	}
}

// Una cartella diventata illeggibile non deve far "cancellare" i file che contiene.
func TestUnreadableDirIsNotDeletion(t *testing.T) {
	env := newTestEnv(t)
	pc := env.newClient("PC1")
	if err := pc.CreateVault("una password lunga e sicura"); err != nil {
		t.Fatal(err)
	}
	writeWork(t, pc, "Privata/a.txt", "A")
	writeWork(t, pc, "Privata/b.txt", "B")
	waitFor(t, "upload", func() bool { return pendingCount(pc) == 0 && env.hubFiles() == 2 })
	dir := filepath.Join(pc.cfg.WorkDir, "Privata")
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)
	time.Sleep(3 * scanInterval)
	pc.mu.Lock()
	for enc, st := range pc.st.Files {
		if st.Del || st.Pending == "del" {
			t.Errorf("file %s segnato come cancellato con la cartella illeggibile", enc[:8])
		}
	}
	pc.mu.Unlock()
	if err := pc.Close(); err == nil {
		t.Error("chiusa con una cartella illeggibile")
	}
}
