package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestTryBeginUpdateRefusesOpenVault(t *testing.T) {
	c := newTestEnv(t).newClient("pc")
	if err := c.CreateVault(testPw); err != nil {
		t.Fatal(err)
	}
	if c.tryBeginUpdate() {
		t.Fatal("aggiornamento iniziato con la cassaforte aperta")
	}
	// rimasta aperta dopo un riavvio: in chiaro sul disco ma senza chiave in memoria
	c.mu.Lock()
	cip := c.cip
	c.cip = nil
	c.mu.Unlock()
	if !c.st.Open {
		t.Fatal("st.Open atteso vero")
	}
	if c.tryBeginUpdate() {
		t.Fatal("aggiornamento iniziato con la cassaforte rimasta aperta")
	}
	c.mu.Lock()
	c.cip = cip
	c.mu.Unlock()
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.busy = "chiusura"
	c.mu.Unlock()
	if c.tryBeginUpdate() {
		t.Fatal("aggiornamento iniziato durante un'operazione")
	}
	c.mu.Lock()
	c.busy = ""
	c.mu.Unlock()
	if !c.tryBeginUpdate() {
		t.Fatal("aggiornamento rifiutato a cassaforte chiusa")
	}
	if c.tryBeginUpdate() {
		t.Fatal("seconda installazione iniziata in parallelo")
	}
}

func TestOpenRefusedWhileUpdating(t *testing.T) {
	c := newTestEnv(t).newClient("pc")
	if err := c.CreateVault(testPw); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.updating = true
	c.mu.Unlock()
	err := c.Open(testPw)
	if err == nil || !strings.Contains(err.Error(), "aggiornamento in corso") {
		t.Fatalf("errore: %v", err)
	}
	if _, err := os.Stat(c.cfg.WorkDir); err == nil {
		t.Fatal("cartella di lavoro creata durante l'aggiornamento")
	}
}

func TestReplaceExeUnix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("solo Unix")
	}
	dir := t.TempDir()
	exe, nw := filepath.Join(dir, "encluca"), filepath.Join(dir, "staged")
	os.WriteFile(exe, []byte("OLD"), 0o755)
	os.WriteFile(nw, []byte("NEW"), 0o700)
	if err := replaceExe(exe, nw); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(exe)
	fi, _ := os.Stat(exe)
	if string(got) != "NEW" || fi.Mode().Perm() != 0o755 {
		t.Fatalf("contenuto %q permessi %o", got, fi.Mode().Perm())
	}
	if _, err := os.Stat(exe + ".new"); err == nil {
		t.Fatal(".new rimasto")
	}
}

func stagedUpdater(t *testing.T, current, staged, exeReports string) *updater {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("usa script shell")
	}
	u, err := newUpdater(t.TempDir(), current, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	u.exe = filepath.Join(t.TempDir(), "encluca")
	os.WriteFile(u.exe, []byte("#!/bin/sh\necho \"Encluca "+exeReports+"\"\n"), 0o755)
	u.stagedPath = filepath.Join(u.dir, exeFileName(runtime.GOOS))
	os.WriteFile(u.stagedPath, []byte("#!/bin/sh\necho \"Encluca "+staged+"\"\n"), 0o755)
	u.st.Staged, u.st.Available = staged, staged
	return u
}

func TestInstallDiscardsObsoleteStaged(t *testing.T) {
	for _, tc := range []struct{ name, current, exeReports string }{
		{"stessa versione in uso", "0.2.1", "0.2.1"},
		{"installata a mano una più nuova", "0.2.0", "0.2.2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := stagedUpdater(t, tc.current, "0.2.1", tc.exeReports)
			before, _ := os.ReadFile(u.exe)
			if _, _, err := u.install(); err == nil {
				t.Fatal("installato un pacchetto non più nuovo")
			}
			if st := u.state(); st.Staged != "" {
				t.Fatalf("stato: %+v", st)
			}
			if _, err := os.Stat(u.stagedPath); err == nil {
				t.Fatal("pacchetto superato non cancellato")
			}
			if after, _ := os.ReadFile(u.exe); string(after) != string(before) {
				t.Fatal("programma sostituito")
			}
		})
	}
}

func TestInstallReplacesAndRecords(t *testing.T) {
	u := stagedUpdater(t, "0.2.0", "0.2.1", "0.2.0")
	from, to, err := u.install()
	if err != nil {
		t.Fatal(err)
	}
	if from != "0.2.0" || to != "0.2.1" {
		t.Fatalf("from %s to %s", from, to)
	}
	if got, _ := os.ReadFile(u.exe); !strings.Contains(string(got), "Encluca 0.2.1") {
		t.Fatalf("programma: %q", got)
	}
	var rec struct{ From, To string }
	if err := loadJSON(filepath.Join(u.dataDir, "update-installed.json"), &rec); err != nil || rec.From != "0.2.0" || rec.To != "0.2.1" {
		t.Fatalf("registro: %+v %v", rec, err)
	}
}

func TestCleanupOldExe(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "encluca")
	for _, p := range []string{exe, exe + ".old", exe + ".new"} {
		os.WriteFile(p, []byte("x"), 0o755)
	}
	cleanupOldExe(exe)
	if _, err := os.Stat(exe); err != nil {
		t.Fatal("programma rimosso")
	}
	for _, p := range []string{exe + ".old", exe + ".new"} {
		if _, err := os.Stat(p); err == nil {
			t.Fatalf("%s rimasto", p)
		}
	}
}

func TestListenRetry(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := busy.Addr().String()
	if _, err := listenRetry(addr, 2, 50*time.Millisecond); err == nil {
		t.Fatal("ascolto riuscito su porta occupata")
	}
	go func() {
		time.Sleep(time.Second)
		busy.Close()
	}()
	ln, err := listenRetry(addr, 20, 200*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
}

func TestAutoUpdateDefaultOn(t *testing.T) {
	c := newBareClient(t)
	if !c.autoUpdateOn() {
		t.Fatal("aggiornamenti automatici spenti di predefinito")
	}
	// config 0.1.x senza il campo
	os.WriteFile(c.cfgPath(), []byte(`{"hubUrl":"","workDir":"/tmp/x"}`), 0o600)
	c2, err := newClient(c.dir)
	if err != nil {
		t.Fatal(err)
	}
	if !c2.autoUpdateOn() {
		t.Fatal("config vecchia: aggiornamenti spenti")
	}
}

func TestSetAutoUpdatePersists(t *testing.T) {
	c := newBareClient(t)
	u, f, priv := newTestUpdater(t, "0.2.0")
	f.release(t, priv, "v0.2.1", "0.2.1", "0.2.1")
	c.upd = u
	if err := c.SetAutoUpdate(false); err != nil {
		t.Fatal(err)
	}
	c2, err := newClient(c.dir)
	if err != nil {
		t.Fatal(err)
	}
	if c2.autoUpdateOn() || c.autoUpdateOn() {
		t.Fatal("impostazione non salvata")
	}
	raw, _ := os.ReadFile(c.cfgPath())
	if !strings.Contains(string(raw), `"autoUpdate": false`) && !strings.Contains(string(raw), `"autoUpdate":false`) {
		t.Fatalf("config: %s", raw)
	}
	c.updateCycle(t.Context())
	if n := f.apiCalls.Load(); n != 0 {
		t.Fatalf("%d controlli con gli aggiornamenti spenti", n)
	}
	if st := c.Status()["update"].(updateState); st.Enabled {
		t.Fatalf("stato: %+v", st)
	}
	if err := c.SetAutoUpdate(true); err != nil {
		t.Fatal(err)
	}
	c.updateCycle(t.Context())
	if n := f.apiCalls.Load(); n != 1 {
		t.Fatalf("%d controlli con gli aggiornamenti accesi", n)
	}
	if st := c.Status()["update"].(updateState); !st.Enabled || st.Current != "0.2.0" {
		t.Fatalf("stato: %+v", st)
	}
}

func TestAutoUpdateAPIRequiresToken(t *testing.T) {
	c := newBareClient(t)
	srv := httptest.NewServer(securityHeaders(c.uiHandler("127.0.0.1:8484")))
	defer srv.Close()
	req, _ := http.NewRequest("POST", srv.URL+"/api/autoupdate", strings.NewReader(`{"On":false}`))
	req.Host = "127.0.0.1:8484"
	req.Header.Set("X-Encluca", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("senza token: %d", resp.StatusCode)
	}
	if !c.autoUpdateOn() {
		t.Fatal("impostazione cambiata senza token")
	}
}
