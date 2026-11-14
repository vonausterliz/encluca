package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// put carica contenuto come nuova revisione del file p (base = revisione attuale).
func put(t *testing.T, env *testEnv, tok, p string, base int, content string) {
	t.Helper()
	if code, body := env.apiCall(t, http.MethodPut, "/api/v1/file?p="+p+"&base="+strconv.Itoa(base), tok, "2", strings.NewReader(content)); code != 200 {
		t.Fatalf("put %s base %d: %d %s", p, base, code, body)
	}
	time.Sleep(2 * time.Millisecond) // istanti distinti tra una revisione e l'altra
}

func del(t *testing.T, env *testEnv, tok, p string, base int) {
	t.Helper()
	if code, body := env.apiCall(t, http.MethodDelete, "/api/v1/file?p="+p+"&base="+strconv.Itoa(base), tok, "2", nil); code != 200 {
		t.Fatalf("delete %s: %d %s", p, code, body)
	}
	time.Sleep(2 * time.Millisecond)
}

// content legge il file corrente dal disco dell'hub ("<assente>" se non c'è).
func content(env *testEnv, p string) string {
	b, err := os.ReadFile(filepath.Join(env.hub.vaultDir(), filepath.FromSlash(p)))
	if err != nil {
		return "<assente>"
	}
	return string(b)
}

func versionContent(env *testEnv, v versionItem) string {
	b, err := os.ReadFile(filepath.Join(env.hub.versionsDir(), v.File))
	if err != nil {
		return "<assente>"
	}
	return string(b)
}

func versions(env *testEnv) []versionItem {
	env.hub.mu.Lock()
	defer env.hub.mu.Unlock()
	return append([]versionItem(nil), env.hub.versions...)
}

func versionFiles(env *testEnv) int {
	ents, _ := os.ReadDir(env.hub.versionsDir())
	return len(ents)
}

func TestOverwriteKeepsPreviousVersion(t *testing.T) {
	env := newTestEnv(t)
	tok := env.deviceToken("A")
	put(t, env, tok, encF, 0, "uno")
	put(t, env, tok, encF, 1, "due")
	vs := versions(env)
	if len(vs) != 1 {
		t.Fatalf("versioni: %d", len(vs))
	}
	v := vs[0]
	if versionContent(env, v) != "uno" || v.Path != encF || v.Rev != 1 || v.Since <= 0 || v.Replaced < v.Since {
		t.Fatalf("versione: %+v contenuto %q", v, versionContent(env, v))
	}
	if content(env, encF) != "due" {
		t.Fatalf("file corrente: %q", content(env, encF))
	}
	if e := env.entry(encF); e == nil || e.At <= 0 {
		t.Fatalf("voce corrente senza data: %+v", e)
	}
}

func TestSameContentKeepsNoVersion(t *testing.T) {
	env := newTestEnv(t)
	tok := env.deviceToken("A")
	put(t, env, tok, encF, 0, "uguale")
	put(t, env, tok, encF, 1, "uguale")
	if n := len(versions(env)); n != 0 {
		t.Fatalf("versioni: %d", n)
	}
}

func TestVersionsKeepSetting(t *testing.T) {
	env := newTestEnv(t)
	tok := env.deviceToken("A")
	for i := 0; i < 7; i++ {
		put(t, env, tok, encF, i, "contenuto "+strconv.Itoa(i))
	}
	vs := versions(env)
	if len(vs) != 5 || versionFiles(env) != 5 {
		t.Fatalf("versioni: %d, file: %d", len(vs), versionFiles(env))
	}
	for _, v := range vs {
		if v.Rev < 2 { // restano le 5 più recenti: revisioni 2..6
			t.Fatalf("rimasta una versione troppo vecchia: rev %d", v.Rev)
		}
	}
	env.hub.mu.Lock()
	env.hub.cfg.Versions = &VersionsConfig{Keep: 2, MaxAgeDays: 90}
	env.hub.pruneVersionsLocked()
	env.hub.mu.Unlock()
	if len(versions(env)) != 2 || versionFiles(env) != 2 {
		t.Fatalf("dopo Keep=2: %d versioni, %d file", len(versions(env)), versionFiles(env))
	}
	env.hub.mu.Lock()
	env.hub.cfg.Versions = &VersionsConfig{Keep: 0, MaxAgeDays: 90}
	env.hub.mu.Unlock()
	put(t, env, tok, encF, 7, "con le versioni disattivate")
	if len(versions(env)) != 2 {
		t.Fatalf("con Keep=0: %d versioni", len(versions(env)))
	}
}

func TestVersionsMaxAge(t *testing.T) {
	env := newTestEnv(t)
	tok := env.deviceToken("A")
	put(t, env, tok, encF, 0, "a")
	put(t, env, tok, encF, 1, "b")
	put(t, env, tok, encY, 0, "c")
	put(t, env, tok, encY, 1, "d")
	env.hub.mu.Lock()
	for i := range env.hub.versions {
		age := 89
		if env.hub.versions[i].Path == encF {
			age = 91
		}
		env.hub.versions[i].Replaced = time.Now().Add(-time.Duration(age) * 24 * time.Hour).UnixNano()
	}
	env.hub.pruneVersionsLocked()
	env.hub.mu.Unlock()
	vs := versions(env)
	if len(vs) != 1 || vs[0].Path != encY || versionFiles(env) != 1 {
		t.Fatalf("dopo la pulizia per età: %+v, file %d", vs, versionFiles(env))
	}
}

func TestVersionSaveNeverRemovesCurrent(t *testing.T) {
	env := newTestEnv(t)
	tok := env.deviceToken("A")
	put(t, env, tok, encF, 0, "corrente")
	full := filepath.Join(env.hub.vaultDir(), encF)
	env.hub.mu.Lock()
	err := env.hub.keepVersionLocked(full, env.hub.idx.Entries[encF], time.Now().UnixNano(), false)
	env.hub.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if content(env, encF) != "corrente" {
		t.Fatal("il file corrente è sparito salvando la versione")
	}
	if vs := versions(env); len(vs) != 1 || versionContent(env, vs[0]) != "corrente" {
		t.Fatalf("versione: %+v", vs)
	}
}

func TestVersionsSurviveRestart(t *testing.T) {
	env := newTestEnv(t)
	tok := env.deviceToken("A")
	put(t, env, tok, encF, 0, "uno")
	put(t, env, tok, encF, 1, "due")
	h2, err := newHub(env.hub.dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(h2.versions) != 1 || h2.versions[0].Path != encF {
		t.Fatalf("versioni dopo il riavvio: %+v", h2.versions)
	}
}

func TestStatusReportsVersions(t *testing.T) {
	env := newTestEnv(t)
	tok := env.deviceToken("A")
	put(t, env, tok, encF, 0, "uno")
	put(t, env, tok, encF, 1, "due e mezzo")
	rec := httptest.NewRecorder()
	env.hub.adminStatus(rec, httptest.NewRequest(http.MethodGet, "/admin/api/status", nil))
	var s struct {
		Versions struct {
			Files      int   `json:"files"`
			Bytes      int64 `json:"bytes"`
			Keep       int   `json:"keep"`
			MaxAgeDays int   `json:"maxAgeDays"`
		} `json:"versions"`
	}
	json.Unmarshal(rec.Body.Bytes(), &s)
	if s.Versions.Files != 1 || s.Versions.Bytes != 3 || s.Versions.Keep != 5 || s.Versions.MaxAgeDays != 90 {
		t.Fatalf("stato: %+v", s.Versions)
	}
}
