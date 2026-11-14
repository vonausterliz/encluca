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

// ---- ripristino ----

func nowNano() int64 {
	n := time.Now().UnixNano()
	time.Sleep(2 * time.Millisecond)
	return n
}

func restoreAt(env *testEnv, at int64, dry bool) (int, int) {
	env.hub.mu.Lock()
	defer env.hub.mu.Unlock()
	return env.hub.restoreAtLocked(at, dry)
}

func TestRestoreSingleVersion(t *testing.T) {
	env := newTestEnv(t)
	tok := env.deviceToken("A")
	put(t, env, tok, encF, 0, "uno")
	put(t, env, tok, encF, 1, "due")
	env.hub.mu.Lock()
	err := env.hub.restoreVersionLocked(0, time.Now().UnixNano())
	env.hub.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if content(env, encF) != "uno" {
		t.Fatalf("file corrente: %q", content(env, encF))
	}
	if e := env.entry(encF); e.Rev != 3 || e.Dev != "versions" || e.Del || e.Size != 3 {
		t.Fatalf("voce: %+v", e)
	}
	// la versione ripristinata resta nella storia, e quella sostituita si aggiunge
	got := map[string]bool{}
	for _, v := range versions(env) {
		got[versionContent(env, v)] = true
	}
	if len(got) != 2 || !got["uno"] || !got["due"] {
		t.Fatalf("versioni dopo il ripristino: %v", got)
	}
}

func TestRestoreAt(t *testing.T) {
	env := newTestEnv(t)
	tok := env.deviceToken("A")
	put(t, env, tok, encF, 0, "a1")
	put(t, env, tok, encY, 0, "b1")
	x := nowNano()
	put(t, env, tok, encF, 1, "a2")
	put(t, env, tok, encF, 2, "a3")
	if c, u := restoreAt(env, x, true); c != 1 || u != 0 {
		t.Fatalf("prova: %d %d", c, u)
	}
	if content(env, encF) != "a3" {
		t.Fatal("la prova ha modificato il file")
	}
	if c, u := restoreAt(env, x, false); c != 1 || u != 0 {
		t.Fatalf("ripristino: %d %d", c, u)
	}
	if content(env, encF) != "a1" || content(env, encY) != "b1" {
		t.Fatalf("contenuti: %q %q", content(env, encF), content(env, encY))
	}
	if e := env.entry(encY); e.Rev != 1 {
		t.Fatalf("il file non modificato è stato toccato: %+v", e)
	}
}

func TestRestoreAtIsReversible(t *testing.T) {
	env := newTestEnv(t)
	tok := env.deviceToken("A")
	put(t, env, tok, encF, 0, "a1")
	x := nowNano()
	put(t, env, tok, encF, 1, "a2")
	put(t, env, tok, encF, 2, "a3")
	y := nowNano()
	restoreAt(env, x, false)
	if content(env, encF) != "a1" {
		t.Fatalf("dopo il primo ripristino: %q", content(env, encF))
	}
	time.Sleep(2 * time.Millisecond)
	if c, _ := restoreAt(env, y, false); c != 1 || content(env, encF) != "a3" {
		t.Fatalf("annullamento: cambiati %d, contenuto %q", c, content(env, encF))
	}
	// e si può tornare di nuovo alla stessa data: la storia non si consuma
	time.Sleep(2 * time.Millisecond)
	if c, _ := restoreAt(env, x, false); c != 1 || content(env, encF) != "a1" {
		t.Fatalf("secondo ritorno a X: cambiati %d, contenuto %q", c, content(env, encF))
	}
}

func TestRestoreAtModifiedThenDeleted(t *testing.T) {
	env := newTestEnv(t)
	tok := env.deviceToken("A")
	put(t, env, tok, encF, 0, "a1")
	x := nowNano()
	put(t, env, tok, encF, 1, "a2")
	del(t, env, tok, encF, 2)
	_, u := restoreAt(env, x, false)
	if u != 1 || content(env, encF) != "a1" {
		t.Fatalf("recuperati %d, contenuto %q", u, content(env, encF))
	}
	if e := env.entry(encF); e.Del {
		t.Fatal("la voce è ancora cancellata")
	}
}

func TestRestoreAtDeletedOnly(t *testing.T) {
	env := newTestEnv(t)
	tok := env.deviceToken("A")
	put(t, env, tok, encF, 0, "a1")
	x := nowNano()
	del(t, env, tok, encF, 1)
	if _, u := restoreAt(env, x, false); u != 1 || content(env, encF) != "a1" {
		t.Fatalf("recuperati %d, contenuto %q", u, content(env, encF))
	}
}

func TestRestoreAtIgnoresUndatedEntries(t *testing.T) {
	env := newTestEnv(t)
	tok := env.deviceToken("A")
	put(t, env, tok, encF, 0, "vecchio")
	env.hub.mu.Lock()
	env.hub.idx.Entries[encF].At = 0 // come una voce caricata prima della 0.2.5
	env.hub.mu.Unlock()
	if c, u := restoreAt(env, time.Now().Add(-time.Hour).UnixNano(), false); c != 0 || u != 0 {
		t.Fatalf("toccata una voce senza data: %d %d", c, u)
	}
	if content(env, encF) != "vecchio" || env.entry(encF).Rev != 1 {
		t.Fatal("voce senza data modificata")
	}
}

func TestRestoreAtKeepsFilesCreatedLater(t *testing.T) {
	env := newTestEnv(t)
	tok := env.deviceToken("A")
	x := nowNano()
	put(t, env, tok, encF, 0, "nuovo")
	put(t, env, tok, encF, 1, "nuovo 2")
	if c, u := restoreAt(env, x, false); c != 0 || u != 0 || content(env, encF) != "nuovo 2" {
		t.Fatalf("file creato dopo: %d %d %q", c, u, content(env, encF))
	}
}

func TestVersionsAdminAPI(t *testing.T) {
	env := newTestEnv(t)
	tok := env.deviceToken("A")
	put(t, env, tok, encF, 0, "a1")
	x := time.Now()
	time.Sleep(5 * time.Millisecond)
	put(t, env, tok, encF, 1, "a2")
	admin := adminClient(t, env)
	base := env.srv.URL + "/admin/api/"

	if code, body := postJSON(t, admin, base+"versions/config", map[string]int{"Keep": 3, "MaxAgeDays": 30}); code != 200 {
		t.Fatalf("config: %d %s", code, body)
	}
	env.hub.mu.Lock()
	cfg := env.hub.versionsCfgLocked()
	env.hub.mu.Unlock()
	if cfg.Keep != 3 || cfg.MaxAgeDays != 30 {
		t.Fatalf("config salvata: %+v", cfg)
	}
	if code, _ := postJSON(t, admin, base+"versions/config", map[string]int{"Keep": 500, "MaxAgeDays": 30}); code != 400 {
		t.Fatalf("Keep fuori limite: %d", code)
	}
	if code, _ := postJSON(t, admin, base+"versions/config", map[string]int{"Keep": 2, "MaxAgeDays": -1}); code != 400 {
		t.Fatalf("MaxAgeDays negativo: %d", code)
	}

	at := x.Format(time.RFC3339Nano)
	code, body := postJSON(t, admin, base+"versions/restore-at", map[string]any{"At": at, "DryRun": true})
	if code != 200 || !strings.Contains(string(body), `"changed":1`) || content(env, encF) != "a2" {
		t.Fatalf("prova: %d %s", code, body)
	}
	if code, _ := postJSON(t, admin, base+"versions/restore-at", map[string]any{"At": time.Now().Add(time.Hour).Format(time.RFC3339)}); code != 400 {
		t.Fatalf("data futura: %d", code)
	}
	if code, _ := postJSON(t, admin, base+"versions/restore-at", map[string]any{"At": "ieri"}); code != 400 {
		t.Fatalf("data non valida: %d", code)
	}

	// le versioni del singolo file richiedono l'esplorazione attiva
	if code, _ := get(t, admin, base+"vault/versions?p="+encF); code != 403 {
		t.Fatalf("elenco con esplorazione spenta: %d", code)
	}
	env.hub.mu.Lock()
	env.hub.cfg.BrowseEnabled = true
	env.hub.mu.Unlock()
	code, body = get(t, admin, base+"vault/versions?p="+encF)
	var list []struct {
		ID       string `json:"id"`
		Replaced int64  `json:"replaced"`
	}
	json.Unmarshal(body, &list)
	if code != 200 || len(list) != 1 || list[0].ID == "" || list[0].Replaced == 0 {
		t.Fatalf("elenco: %d %s", code, body)
	}
	if code, _ := postJSON(t, admin, base+"vault/versions/restore", map[string]string{"P": encF, "ID": "inesistente"}); code != 404 {
		t.Fatalf("versione inesistente: %d", code)
	}
	if code, body := postJSON(t, admin, base+"vault/versions/restore", map[string]string{"P": encF, "ID": list[0].ID}); code != 200 || content(env, encF) != "a1" {
		t.Fatalf("ripristino singolo: %d %s contenuto %q", code, body, content(env, encF))
	}
	// e il ripristino a una data dall'API
	time.Sleep(5 * time.Millisecond)
	put(t, env, tok, encF, 3, "a4")
	if code, body := postJSON(t, admin, base+"versions/restore-at", map[string]any{"At": at}); code != 200 || content(env, encF) != "a1" {
		t.Fatalf("restore-at: %d %s contenuto %q", code, body, content(env, encF))
	}
}

// Il ripristino crea una nuova revisione: i PC la ricevono come una modifica qualunque.
func TestRestoreAtReachesClients(t *testing.T) {
	env, a, b := twoOpen(t)
	writeWork(t, a, "doc.txt", "versione buona")
	waitFor(t, "prima versione su B", func() bool { return readWork(b, "doc.txt") == "versione buona" })
	x := nowNano()
	writeWork(t, a, "doc.txt", "rovinato da un virus")
	waitFor(t, "seconda versione su B", func() bool { return readWork(b, "doc.txt") == "rovinato da un virus" })
	waitFor(t, "niente in coda", func() bool { return pendingCount(a) == 0 && pendingCount(b) == 0 })
	if c, _ := restoreAt(env, x, false); c != 1 {
		t.Fatalf("file riportati: %d", c)
	}
	waitFor(t, "ripristino su A e B", func() bool {
		return readWork(a, "doc.txt") == "versione buona" && readWork(b, "doc.txt") == "versione buona"
	})
	waitFor(t, "niente in coda", func() bool { return pendingCount(a) == 0 && pendingCount(b) == 0 })
	for _, c := range []*Client{a, b} {
		for _, f := range listWork(c) {
			if strings.Contains(f, "conflitto") {
				t.Fatalf("copia di conflitto: %s", f)
			}
		}
	}
}

// Un file eliminato e poi ricreato con lo stesso nome (come fanno alcuni ransomware): il
// contenuto di prima è nel cestino, e "riporta com'era" deve ritrovarlo anche se il file c'è.
func TestRestoreAtDeletedThenRecreated(t *testing.T) {
	env := newTestEnv(t)
	tok := env.deviceToken("A")
	put(t, env, tok, encF, 0, "originale")
	x := nowNano()
	del(t, env, tok, encF, 1)
	put(t, env, tok, encF, 2, "cifrato dal virus")
	if c, _ := restoreAt(env, x, true); c != 1 {
		t.Fatalf("prova: cambiati %d", c)
	}
	if c, _ := restoreAt(env, x, false); c != 1 || content(env, encF) != "originale" {
		t.Fatalf("cambiati %d, contenuto %q", c, content(env, encF))
	}
	// il contenuto sostituito resta tra le versioni: il ripristino è annullabile
	found := false
	for _, v := range versions(env) {
		if versionContent(env, v) == "cifrato dal virus" {
			found = true
		}
	}
	if !found {
		t.Fatal("il contenuto sostituito non è stato conservato")
	}
	env.hub.mu.Lock()
	n := len(env.hub.trash)
	env.hub.mu.Unlock()
	if n != 0 {
		t.Fatalf("elemento rimasto nel cestino: %d", n)
	}
}

// Un file nato dopo la data scelta, poi eliminato e ricreato, non esisteva a quella data: resta com'è.
func TestRestoreAtIgnoresTrashOfLaterFiles(t *testing.T) {
	env := newTestEnv(t)
	tok := env.deviceToken("A")
	x := nowNano()
	put(t, env, tok, encF, 0, "nato dopo")
	del(t, env, tok, encF, 1)
	put(t, env, tok, encF, 2, "ricreato")
	if c, _ := restoreAt(env, x, false); c != 0 || content(env, encF) != "ricreato" {
		t.Fatalf("cambiati %d, contenuto %q", c, content(env, encF))
	}
}
