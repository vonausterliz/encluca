package main

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSafeRel(t *testing.T) {
	ok := []string{"a.txt", "Documenti/Tasse/730.txt", "..nome", "a..b/c", "spazio finale /x"}
	bad := []string{"", "..", "../x", "a/../x", "a/./x", "/assoluto", "a//b", "a/", "a\x00b", "."}
	for _, p := range ok {
		if !safeRelFor("linux", p) {
			t.Errorf("linux: %q deve essere accettato", p)
		}
	}
	for _, p := range bad {
		for _, goos := range []string{"linux", "darwin", "windows"} {
			if safeRelFor(goos, p) {
				t.Errorf("%s: %q deve essere rifiutato", goos, p)
			}
		}
	}
	// su Windows la barra rovesciata separa le cartelle e i due punti aprono flussi nascosti
	for _, p := range []string{`..\..\x`, `a\b`, `C:x`, `file.txt:nascosto`, `a/b\c`} {
		if safeRelFor("windows", p) {
			t.Errorf("windows: %q deve essere rifiutato", p)
		}
	}
	if !safeRelFor("linux", `a\b`) {
		t.Error(`linux: a\b è un nome di file valido`)
	}
}

// Un dispositivo che conosce la password (un PC compromesso) non deve poter scrivere
// o cancellare file fuori dalla cartella aperta degli altri PC.
func TestRemoteNameCannotEscapeWorkDir(t *testing.T) {
	env, a, b := twoOpen(t)
	outside := filepath.Dir(b.cfg.WorkDir)
	victim := filepath.Join(outside, "vittima.txt")
	if err := os.WriteFile(victim, []byte("non toccare"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	up := a.cip.EncryptSegment("..")
	encNew := up + "/" + a.cip.EncryptSegment("fuori.txt")
	encVictim := up + "/" + a.cip.EncryptSegment("vittima.txt")
	encDir := up + "/" + a.cip.EncryptSegment("cartella-fuori")
	body, _ := a.cip.Seal([]byte("contenuto malevolo"))
	a.mu.Unlock()
	tok := env.deviceToken("malevolo")
	for _, p := range []string{encNew, encVictim} {
		if code, out := env.apiCall(t, http.MethodPut, "/api/v1/file?p="+p+"&base=0", tok, "2", bytes.NewReader(body)); code != 200 {
			t.Fatalf("put: %d %s", code, out)
		}
	}
	env.apiCall(t, http.MethodPut, "/api/v1/file?p="+encDir+"&base=0&dir=1", tok, "2", nil)
	env.apiCall(t, http.MethodDelete, "/api/v1/file?p="+encVictim+"&base=1", tok, "2", nil)
	// un file normale caricato dopo: quando arriva su B, le voci malevole sono già state elaborate
	writeWork(t, a, "normale.txt", "ok")
	waitFor(t, "file normale su B", func() bool { return readWork(b, "normale.txt") == "ok" })
	time.Sleep(time.Second)
	if got, _ := os.ReadFile(victim); string(got) != "non toccare" {
		t.Fatalf("il file fuori dalla cartella è stato modificato o cancellato: %q", got)
	}
	for _, name := range []string{"fuori.txt", "cartella-fuori"} {
		if _, err := os.Stat(filepath.Join(outside, name)); err == nil {
			t.Fatalf("%s è stato creato fuori dalla cartella aperta", name)
		}
	}
}

// La pulizia delle cartelle rimaste vuote toglie solo i file di servizio del sistema
// operativo, mai un download in corso o un file di blocco di un programma aperto.
func TestRemoveIfOnlyJunkKeepsUserFiles(t *testing.T) {
	d := filepath.Join(t.TempDir(), "x")
	os.Mkdir(d, 0o700)
	os.WriteFile(filepath.Join(d, ".DS_Store"), []byte("x"), 0o600)
	os.WriteFile(filepath.Join(d, "film.mkv.part"), []byte("download in corso"), 0o600)
	if removeIfOnlyJunk(d) {
		t.Fatal("cartella con un download in corso rimossa")
	}
	if _, err := os.Stat(filepath.Join(d, "film.mkv.part")); err != nil {
		t.Fatal("download in corso cancellato")
	}
	os.Remove(filepath.Join(d, "film.mkv.part"))
	os.WriteFile(filepath.Join(d, "Thumbs.db"), []byte("x"), 0o600)
	if !removeIfOnlyJunk(d) {
		t.Fatal("cartella con soli file di sistema non rimossa")
	}
}
