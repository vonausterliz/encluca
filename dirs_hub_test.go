package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// nomi già "cifrati" (base32hex minuscolo), accettati da validEncPath
const (
	encY = "0123456789abcdef"
	encF = "ghijklmnopqrstuv"
)

func (env *testEnv) entry(p string) *Entry {
	env.hub.mu.Lock()
	defer env.hub.mu.Unlock()
	e := env.hub.idx.Entries[p]
	if e == nil {
		return nil
	}
	c := *e
	return &c
}

func TestHubDirPutDelete(t *testing.T) {
	env := newTestEnv(t)
	tok := env.deviceToken("A")
	if code, body := env.apiCall(t, http.MethodPut, "/api/v1/file?p="+encY+"&base=0&dir=1&mtime=5", tok, "2", nil); code != 200 {
		t.Fatalf("put: %d %s", code, body)
	}
	e := env.entry(encY)
	if e == nil || !e.Dir || e.Rev != 1 || e.Size != 0 || e.SHA != "" || e.Del {
		t.Fatalf("voce: %+v", e)
	}
	full := filepath.Join(env.hub.vaultDir(), encY)
	if fi, err := os.Stat(full); err != nil || !fi.IsDir() {
		t.Fatalf("cartella sul disco: %v", err)
	}
	if code, _ := env.apiCall(t, http.MethodGet, "/api/v1/file?p="+encY, tok, "2", nil); code != http.StatusNotFound {
		t.Fatalf("get di una cartella: %d", code)
	}
	if code, body := env.apiCall(t, http.MethodDelete, "/api/v1/file?p="+encY+"&base=1", tok, "2", nil); code != 200 {
		t.Fatalf("delete: %d %s", code, body)
	}
	e = env.entry(encY)
	if e == nil || !e.Dir || !e.Del || e.Rev != 2 {
		t.Fatalf("voce cancellata: %+v", e)
	}
	if _, err := os.Stat(full); !os.IsNotExist(err) {
		t.Fatal("la cartella è ancora sul disco")
	}
	env.hub.mu.Lock()
	defer env.hub.mu.Unlock()
	if len(env.hub.trash) != 1 || !env.hub.trash[0].Dir || env.hub.trash[0].Path != encY {
		t.Fatalf("cestino: %+v", env.hub.trash)
	}
}

func TestHubDirPutConflict(t *testing.T) {
	env := newTestEnv(t)
	tok := env.deviceToken("A")
	env.apiCall(t, http.MethodPut, "/api/v1/file?p="+encY+"&base=0&dir=1", tok, "2", nil)
	code, body := env.apiCall(t, http.MethodPut, "/api/v1/file?p="+encY+"&base=0&dir=1", tok, "2", nil)
	if code != http.StatusConflict || !strings.Contains(body, `"dir":true`) {
		t.Fatalf("secondo put: %d %s", code, body)
	}
}

func TestHubParentCleanupKeepsLiveDir(t *testing.T) {
	env := newTestEnv(t)
	tok := env.deviceToken("A")
	env.apiCall(t, http.MethodPut, "/api/v1/file?p="+encY+"/"+encF+"&base=0", tok, "2", strings.NewReader("dati"))
	env.apiCall(t, http.MethodPut, "/api/v1/file?p="+encY+"&base=0&dir=1", tok, "2", nil)
	if code, body := env.apiCall(t, http.MethodDelete, "/api/v1/file?p="+encY+"/"+encF+"&base=1", tok, "2", nil); code != 200 {
		t.Fatalf("delete file: %d %s", code, body)
	}
	if _, err := os.Stat(filepath.Join(env.hub.vaultDir(), encY)); err != nil {
		t.Fatal("la cartella con voce viva è stata rimossa dal disco")
	}
}

func TestHubRestoreDirFromTrash(t *testing.T) {
	env := newTestEnv(t)
	tok := env.deviceToken("A")
	env.apiCall(t, http.MethodPut, "/api/v1/file?p="+encY+"&base=0&dir=1", tok, "2", nil)
	env.apiCall(t, http.MethodDelete, "/api/v1/file?p="+encY+"&base=1", tok, "2", nil)
	env.hub.mu.Lock()
	n := env.hub.restoreTrashLocked(0)
	env.hub.mu.Unlock()
	e := env.entry(encY)
	if n != 1 || e == nil || !e.Dir || e.Del || e.Rev != 3 {
		t.Fatalf("ripristino: n=%d voce=%+v", n, e)
	}
	if fi, err := os.Stat(filepath.Join(env.hub.vaultDir(), encY)); err != nil || !fi.IsDir() {
		t.Fatal("cartella non ricreata sul disco")
	}
}

func TestHubRebuildIndexDirs(t *testing.T) {
	env := newTestEnv(t)
	v := env.hub.vaultDir()
	os.MkdirAll(filepath.Join(v, encY), 0o700)
	os.MkdirAll(filepath.Join(v, encF), 0o700)
	os.WriteFile(filepath.Join(v, encF, encY), []byte("x"), 0o600)
	env.hub.mu.Lock()
	err := env.hub.rebuildIndex()
	env.hub.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if e := env.entry(encY); e == nil || !e.Dir {
		t.Fatalf("cartella vuota: %+v", e)
	}
	if e := env.entry(encF); e != nil {
		t.Fatalf("la cartella con file non deve avere una voce: %+v", e)
	}
	if e := env.entry(encF + "/" + encY); e == nil || e.Dir {
		t.Fatalf("file: %+v", e)
	}
}

func TestHubStatusCountsOnlyFiles(t *testing.T) {
	env := newTestEnv(t)
	tok := env.deviceToken("A")
	env.apiCall(t, http.MethodPut, "/api/v1/file?p="+encY+"&base=0&dir=1", tok, "2", nil)
	env.apiCall(t, http.MethodPut, "/api/v1/file?p="+encF+"&base=0", tok, "2", strings.NewReader("dati"))
	rec := httptest.NewRecorder()
	env.hub.adminStatus(rec, httptest.NewRequest(http.MethodGet, "/admin/api/status", nil))
	var s struct{ Files int }
	json.Unmarshal(rec.Body.Bytes(), &s)
	if s.Files != 1 {
		t.Fatalf("files = %d, voglio 1", s.Files)
	}
}

func TestBrowseListIncludesDirs(t *testing.T) {
	env := newTestEnv(t)
	tok := env.deviceToken("A")
	env.apiCall(t, http.MethodPut, "/api/v1/file?p="+encY+"&base=0&dir=1", tok, "2", nil)
	rec := httptest.NewRecorder()
	env.hub.apiBrowseList(rec, httptest.NewRequest(http.MethodGet, "/admin/api/vault/list", nil))
	if !strings.Contains(rec.Body.String(), `"p":"`+encY+`"`) || !strings.Contains(rec.Body.String(), `"dir":true`) {
		t.Fatalf("elenco: %s", rec.Body.String())
	}
}
