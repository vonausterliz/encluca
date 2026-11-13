package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeGitHub simula l'API "latest" di GitHub e i file della release.
type fakeGitHub struct {
	srv       *httptest.Server
	tag       string
	files     map[string][]byte
	downloads atomic.Int32
	apiCalls  atomic.Int32
	truncate  string // nome del file da troncare a metà
	oversize  string // nome del file da servire più grande del limite
}

func (f *fakeGitHub) handler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/latest" {
		f.apiCalls.Add(1)
		type asset struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		}
		var as []asset
		for n := range f.files {
			as = append(as, asset{n, f.srv.URL + "/dl/" + n})
		}
		json.NewEncoder(w).Encode(map[string]any{"tag_name": f.tag, "assets": as})
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/dl/")
	data, ok := f.files[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	f.downloads.Add(1)
	if name == f.oversize {
		chunk := make([]byte, 1<<20)
		for i := 0; i <= maxArchiveSize>>20; i++ {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
		return
	}
	if name == f.truncate {
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.Write(data[:len(data)/2])
		return // la risposta resta incompleta: il client riceve un EOF inatteso
	}
	w.Write(data)
}

// release prepara una release firmata con priv: l'archivio contiene uno
// script che stampa "Encluca <printVer>" (per la prova di esecuzione).
func (f *fakeGitHub) release(t *testing.T, priv ed25519.PrivateKey, tag, pkgVer, printVer string) {
	t.Helper()
	v := strings.TrimPrefix(pkgVer, "v")
	script := []byte("#!/bin/sh\necho \"Encluca " + printVer + "\"\n")
	arch := makeTarGz(t, map[string][]byte{
		"encluca-" + v + "-linux-amd64/encluca":    script,
		"encluca-" + v + "-linux-amd64/LEGGIMI.md": []byte("x"),
	}, nil)
	name := assetName(v, "linux", "amd64")
	sum := sha256.Sum256(arch)
	sums := hex.EncodeToString(sum[:]) + "  " + name + "\n"
	f.tag = tag
	f.files = map[string][]byte{
		name:             arch,
		"SHA256SUMS":     []byte(sums),
		"SHA256SUMS.sig": signed(t, priv, sums),
	}
}

func newTestUpdater(t *testing.T, current string) (*updater, *fakeGitHub, ed25519.PrivateKey) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("la prova di esecuzione usa uno script shell")
	}
	f := &fakeGitHub{}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.handler))
	t.Cleanup(f.srv.Close)
	data := t.TempDir()
	u, err := newUpdater(data, current, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	u.pub = pub
	u.api = f.srv.URL + "/latest"
	u.hc = f.srv.Client()
	u.goos, u.goarch = "linux", "amd64"
	exeDir := t.TempDir()
	u.exe = filepath.Join(exeDir, "encluca")
	os.WriteFile(u.exe, []byte("OLD"), 0o755)
	return u, f, priv
}

func TestCheckStagesNewer(t *testing.T) {
	u, f, priv := newTestUpdater(t, "0.2.0")
	f.release(t, priv, "v0.2.1", "0.2.1", "0.2.1")
	if err := u.check(t.Context()); err != nil {
		t.Fatal(err)
	}
	st := u.state()
	if st.Staged != "0.2.1" || st.Available != "0.2.1" || st.Error != "" || st.LastCheck.IsZero() {
		t.Fatalf("stato: %+v", st)
	}
	got, _ := os.ReadFile(u.stagedPath)
	if !strings.Contains(string(got), "Encluca 0.2.1") {
		t.Fatalf("file preparato: %q", got)
	}
	// un secondo controllo con la stessa versione pronta non riscarica nulla
	n := f.downloads.Load()
	if err := u.check(t.Context()); err != nil || f.downloads.Load() != n {
		t.Fatalf("secondo controllo: err=%v download %d→%d", err, n, f.downloads.Load())
	}
}

func TestCheckIgnoresSameOrOlder(t *testing.T) {
	u, f, priv := newTestUpdater(t, "0.2.0")
	for _, tag := range []string{"v0.2.0", "v0.1.9"} {
		f.release(t, priv, tag, tag, tag)
		if err := u.check(t.Context()); err != nil {
			t.Fatal(err)
		}
		if st := u.state(); st.Staged != "" || st.Available != "" {
			t.Fatalf("%s: stato %+v", tag, st)
		}
	}
	if n := f.downloads.Load(); n != 0 {
		t.Fatalf("%d download per versioni non più nuove", n)
	}
}

func TestCheckRejectsBadSignature(t *testing.T) {
	u, f, _ := newTestUpdater(t, "0.2.0")
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	f.release(t, other, "v0.2.1", "0.2.1", "0.2.1")
	if err := u.check(t.Context()); err == nil {
		t.Fatal("firma di un'altra chiave accettata")
	} else if !strings.Contains(err.Error(), "firma") {
		t.Fatalf("errore inatteso: %v", err)
	}
	if st := u.state(); st.Staged != "" || st.Error == "" {
		t.Fatalf("stato: %+v", st)
	}
	assertNoLeftovers(t, u)
}

func TestCheckRejectsHashMismatch(t *testing.T) {
	u, f, priv := newTestUpdater(t, "0.2.0")
	f.release(t, priv, "v0.2.1", "0.2.1", "0.2.1")
	name := assetName("0.2.1", "linux", "amd64")
	f.files[name] = append([]byte{}, f.files[name]...)
	f.files[name][len(f.files[name])-1] ^= 1
	if err := u.check(t.Context()); err == nil || !strings.Contains(err.Error(), "impronta firmata") {
		t.Fatalf("archivio modificato: errore %v", err)
	}
	if st := u.state(); st.Staged != "" {
		t.Fatalf("stato: %+v", st)
	}
	assertNoLeftovers(t, u)
}

func TestCheckRejectsReplayedOldRelease(t *testing.T) {
	u, f, priv := newTestUpdater(t, "0.2.0")
	f.release(t, priv, "v0.1.9", "0.1.9", "0.1.9")
	f.tag = "v9.9.9" // release vecchia firmata ripresentata con un tag nuovo
	err := u.check(t.Context())
	if err == nil || !strings.Contains(err.Error(), "encluca-9.9.9-linux-amd64.tar.gz") {
		t.Fatalf("errore: %v", err)
	}
	if st := u.state(); st.Staged != "" {
		t.Fatalf("stato: %+v", st)
	}
}

func TestCheckRejectsOversize(t *testing.T) {
	u, f, priv := newTestUpdater(t, "0.2.0")
	f.release(t, priv, "v0.2.1", "0.2.1", "0.2.1")
	f.oversize = assetName("0.2.1", "linux", "amd64")
	if err := u.check(t.Context()); err == nil || !strings.Contains(err.Error(), "troppo grande") {
		t.Fatalf("archivio troppo grande: errore %v", err)
	}
	assertNoLeftovers(t, u)
}

func TestCheckTruncatedDownload(t *testing.T) {
	u, f, priv := newTestUpdater(t, "0.2.0")
	f.release(t, priv, "v0.2.1", "0.2.1", "0.2.1")
	f.truncate = assetName("0.2.1", "linux", "amd64")
	if err := u.check(t.Context()); err == nil {
		t.Fatal("download troncato accettato")
	}
	if st := u.state(); st.Staged != "" {
		t.Fatalf("stato: %+v", st)
	}
	assertNoLeftovers(t, u)
	f.truncate = ""
	if err := u.check(t.Context()); err != nil {
		t.Fatal(err)
	}
	if st := u.state(); st.Staged != "0.2.1" || st.Error != "" {
		t.Fatalf("stato dopo il nuovo tentativo: %+v", st)
	}
}

func TestCheckExeNotWritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("da root i permessi delle cartelle non valgono")
	}
	u, f, priv := newTestUpdater(t, "0.2.0")
	f.release(t, priv, "v0.2.1", "0.2.1", "0.2.1")
	dir := filepath.Dir(u.exe)
	os.Chmod(dir, 0o500)
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	err := u.check(t.Context())
	if err == nil || !strings.Contains(err.Error(), "non posso sostituire il programma") {
		t.Fatalf("errore: %v", err)
	}
	if n := f.downloads.Load(); n != 0 {
		t.Fatalf("%d download inutili", n)
	}
	if u.state().Error == "" {
		t.Fatal("errore non riportato nello stato")
	}
}

func TestCheckSanityRun(t *testing.T) {
	u, f, priv := newTestUpdater(t, "0.2.0")
	f.release(t, priv, "v0.2.1", "0.2.1", "0.2.0") // il programma dichiara un'altra versione
	if err := u.check(t.Context()); err == nil {
		t.Fatal("programma con versione sbagliata accettato")
	}
	if st := u.state(); st.Staged != "" {
		t.Fatalf("stato: %+v", st)
	}
	assertNoLeftovers(t, u)
}

func assertNoLeftovers(t *testing.T, u *updater) {
	t.Helper()
	ents, _ := os.ReadDir(u.dir)
	if len(ents) != 0 {
		var names []string
		for _, e := range ents {
			names = append(names, e.Name())
		}
		t.Fatalf("file rimasti in %s: %v", u.dir, names)
	}
}
