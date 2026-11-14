package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const dirPw = "una password lunga e sicura"

// twoOpen prepara due PC abbinati allo stesso NAS, con la cassaforte aperta su entrambi.
func twoOpen(t *testing.T) (*testEnv, *Client, *Client) {
	env := newTestEnv(t)
	a := env.newClient("A")
	if err := a.CreateVault(dirPw); err != nil {
		t.Fatal(err)
	}
	b := env.newClient("B")
	if err := b.Open(dirPw); err != nil {
		t.Fatal(err)
	}
	return env, a, b
}

func workPath(c *Client, rel string) string {
	return filepath.Join(c.cfg.WorkDir, filepath.FromSlash(rel))
}

func isDir(c *Client, rel string) bool {
	fi, err := os.Stat(workPath(c, rel))
	return err == nil && fi.IsDir()
}

func exists(c *Client, rel string) bool {
	_, err := os.Stat(workPath(c, rel))
	return err == nil
}

// hubEntry restituisce la voce dell'hub per un percorso in chiaro (cifrato con la chiave di c).
func hubEntry(env *testEnv, c *Client, rel string) *Entry {
	c.mu.Lock()
	enc, err := c.cip.EncryptPath(rel)
	c.mu.Unlock()
	if err != nil {
		return nil
	}
	return env.entry(enc)
}

func liveDir(env *testEnv, c *Client, rel string) bool {
	e := hubEntry(env, c, rel)
	return e != nil && e.Dir && !e.Del
}

func liveDirCount(env *testEnv) int {
	env.hub.mu.Lock()
	defer env.hub.mu.Unlock()
	n := 0
	for _, e := range env.hub.idx.Entries {
		if e.Dir && !e.Del {
			n++
		}
	}
	return n
}

func TestEmptyDirSyncs(t *testing.T) {
	env, a, b := twoOpen(t)
	os.MkdirAll(workPath(a, "Progetti/2027"), 0o700)
	waitFor(t, "cartella vuota su B", func() bool { return isDir(b, "Progetti/2027") })
	if !liveDir(env, a, "Progetti/2027") || liveDir(env, a, "Progetti") || liveDirCount(env) != 1 {
		t.Fatalf("voci cartella sull'hub: %d", liveDirCount(env))
	}
}

func TestDirEntryDroppedWhenFilled(t *testing.T) {
	env, a, b := twoOpen(t)
	os.MkdirAll(workPath(a, "Progetti/2027"), 0o700)
	waitFor(t, "voce cartella", func() bool { return liveDir(env, a, "Progetti/2027") })
	writeWork(t, a, "Progetti/2027/a.txt", "ciao")
	waitFor(t, "file su B", func() bool { return readWork(b, "Progetti/2027/a.txt") == "ciao" })
	waitFor(t, "voce cartella cancellata", func() bool {
		e := hubEntry(env, a, "Progetti/2027")
		return e != nil && e.Del
	})
}

func TestEmptyDirDeleted(t *testing.T) {
	_, a, b := twoOpen(t)
	os.MkdirAll(workPath(a, "Vuota"), 0o700)
	waitFor(t, "cartella su B", func() bool { return isDir(b, "Vuota") })
	os.Remove(workPath(a, "Vuota"))
	waitFor(t, "cartella sparita su B", func() bool { return !exists(b, "Vuota") })
}

func TestDirCreatedOnTwoClients(t *testing.T) {
	env, a, b := twoOpen(t)
	os.MkdirAll(workPath(a, "Insieme"), 0o700)
	os.MkdirAll(workPath(b, "Insieme"), 0o700)
	waitFor(t, "voce cartella", func() bool { return liveDir(env, a, "Insieme") })
	waitFor(t, "niente in coda", func() bool { return pendingCount(a) == 0 && pendingCount(b) == 0 })
	for _, c := range []*Client{a, b} {
		for _, f := range listWork(c) {
			if strings.Contains(f, "conflitto") {
				t.Fatalf("copia di conflitto: %s", f)
			}
		}
		if !isDir(c, "Insieme") {
			t.Fatal("cartella sparita")
		}
	}
	if liveDirCount(env) != 1 {
		t.Fatalf("voci cartella: %d", liveDirCount(env))
	}
}

func TestDirOnlyJunkIsEmpty(t *testing.T) {
	env, a, b := twoOpen(t)
	writeWork(t, a, "Mac/.DS_Store", "spazzatura")
	waitFor(t, "cartella su B", func() bool { return isDir(b, "Mac") })
	if !liveDir(env, a, "Mac") {
		t.Fatal("manca la voce cartella")
	}
	if exists(b, "Mac/.DS_Store") {
		t.Fatal(".DS_Store non deve arrivare")
	}
}

func TestEmptyDirSurvivesCloseOpen(t *testing.T) {
	env, a, _ := twoOpen(t)
	os.MkdirAll(workPath(a, "Resta"), 0o700)
	waitFor(t, "voce cartella", func() bool { return liveDir(env, a, "Resta") })
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if err := a.Open(dirPw); err != nil {
		t.Fatal(err)
	}
	if !isDir(a, "Resta") {
		t.Fatal("cartella non ricreata all'apertura")
	}
	a.mu.Lock()
	a.scanLocked(false)
	a.mu.Unlock()
	waitFor(t, "niente in coda", func() bool { return pendingCount(a) == 0 })
	if !liveDir(env, a, "Resta") {
		t.Fatal("la voce cartella è stata cancellata")
	}
}

func TestMassDeleteCountsDirs(t *testing.T) {
	env, a, _ := twoOpen(t)
	for i := 0; i < 30; i++ {
		os.MkdirAll(workPath(a, "Molte/"+string(rune('a'+i%26))+string(rune('a'+i/26))), 0o700)
	}
	waitFor(t, "30 voci cartella", func() bool { return liveDirCount(env) == 30 })
	os.RemoveAll(workPath(a, "Molte"))
	waitFor(t, "eliminazione sospesa", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.massDel > 0
	})
	if liveDirCount(env) != 30 {
		t.Fatal("le cartelle sono state cancellate senza conferma")
	}
}

func TestDirNoResurrection(t *testing.T) {
	env, a, b := twoOpen(t)
	writeWork(t, a, "Foto/x.jpg", "x")
	writeWork(t, a, "Foto/sub/y.jpg", "y")
	waitFor(t, "file su B", func() bool { return readWork(b, "Foto/sub/y.jpg") == "y" })
	writeWork(t, b, "Foto/.DS_Store", "spazzatura")
	os.RemoveAll(workPath(a, "Foto"))
	waitFor(t, "Foto sparita su B", func() bool { return !exists(b, "Foto") })
	waitFor(t, "niente in coda", func() bool { return pendingCount(a) == 0 && pendingCount(b) == 0 })
	b.mu.Lock()
	b.scanLocked(false)
	b.mu.Unlock()
	waitFor(t, "niente in coda", func() bool { return pendingCount(b) == 0 })
	if exists(a, "Foto") || exists(b, "Foto") || liveDirCount(env) != 0 {
		t.Fatalf("Foto è risorta (voci cartella: %d)", liveDirCount(env))
	}
}

func TestDirKeptAfterLastFileDeleted(t *testing.T) {
	env, a, b := twoOpen(t)
	writeWork(t, a, "Y/f.txt", "f")
	waitFor(t, "file su B", func() bool { return readWork(b, "Y/f.txt") == "f" })
	os.Remove(workPath(a, "Y/f.txt"))
	waitFor(t, "file sparito su B", func() bool { return !exists(b, "Y/f.txt") })
	waitFor(t, "voce cartella Y", func() bool { return liveDir(env, a, "Y") })
	waitFor(t, "niente in coda", func() bool { return pendingCount(a) == 0 && pendingCount(b) == 0 })
	if !isDir(a, "Y") || !isDir(b, "Y") {
		t.Fatalf("Y su A=%v su B=%v", isDir(a, "Y"), isDir(b, "Y"))
	}
}

func TestFileReplacesEmptyDir(t *testing.T) {
	_, a, b := twoOpen(t)
	os.MkdirAll(workPath(a, "N"), 0o700)
	waitFor(t, "cartella su B", func() bool { return isDir(b, "N") })
	os.Remove(workPath(a, "N"))
	writeWork(t, a, "N", "ora sono un file")
	waitFor(t, "file N su B", func() bool { return readWork(b, "N") == "ora sono un file" })
	for _, l := range b.log.Lines() {
		if strings.Contains(l, "ERRORE") {
			t.Fatalf("errore nel registro di B: %s", l)
		}
	}
}

func TestEmptyDirArrivesWhileClosed(t *testing.T) {
	env, a, b := twoOpen(t)
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(workPath(a, "Mentre"), 0o700)
	waitFor(t, "voce cartella", func() bool { return liveDir(env, a, "Mentre") })
	waitFor(t, "B allineato", func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		env.hub.mu.Lock()
		defer env.hub.mu.Unlock()
		return b.st.Seq >= env.hub.idx.Seq
	})
	if err := b.Open(dirPw); err != nil {
		t.Fatal(err)
	}
	if !isDir(b, "Mentre") {
		t.Fatal("cartella assente dopo l'apertura")
	}
}
