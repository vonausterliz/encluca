package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// setHubBackup imposta sull'hub di prova autorizzazione e ultimo backup riuscito.
func setHubBackup(env *testEnv, authorized bool, lastOK time.Time) {
	env.hub.mu.Lock()
	if authorized {
		env.hub.cfg.GDrive.RefreshToken = "x"
	} else {
		env.hub.cfg.GDrive.RefreshToken = ""
	}
	env.hub.mu.Unlock()
	env.hub.gd.mu.Lock()
	env.hub.gd.lastOK = lastOK
	env.hub.gd.mu.Unlock()
}

func TestHubBackupEndpoint(t *testing.T) {
	env := newTestEnv(t)
	c := env.newClient("PC1")
	ok := time.Now().Add(-time.Hour).Round(time.Second)
	setHubBackup(env, true, ok)
	var bi backupInfo
	if err := c.getJSON(context.Background(), "/api/v1/backup", nil, &bi); err != nil {
		t.Fatal(err)
	}
	if !bi.Enabled || !bi.LastOK.Equal(ok) {
		t.Fatalf("stato del backup: %+v", bi)
	}
	insecure := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	resp, err := insecure.Get(env.srv.URL + "/api/v1/backup")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("senza token del dispositivo: %d", resp.StatusCode)
	}
}

func alertIDs(c *Client) map[string]alert {
	out := map[string]alert{}
	for _, a := range c.Status()["alerts"].([]alert) {
		out[a.ID] = a
	}
	return out
}

func TestAlertOfflineStartsAtFirstFailure(t *testing.T) {
	env := newTestEnv(t)
	c := env.newClient("PC1")
	env.down.Store(true)
	waitFor(t, "NAS irraggiungibile", func() bool { c.mu.Lock(); defer c.mu.Unlock(); return !c.online })
	if _, ok := alertIDs(c)["offline"]; ok {
		t.Fatal("avviso offline immediato")
	}
	c.mu.Lock()
	since := c.offlineSince
	c.mu.Unlock()
	if time.Since(since) > time.Minute {
		t.Fatalf("offlineSince non impostato al primo errore: %v", since)
	}
}

func TestAlertOffline(t *testing.T) {
	env := newTestEnv(t)
	c := env.newClient("PC1")
	env.down.Store(true)
	waitFor(t, "NAS irraggiungibile", func() bool { c.mu.Lock(); defer c.mu.Unlock(); return !c.online })
	c.mu.Lock()
	c.offlineSince = time.Now().Add(-3 * time.Hour)
	c.mu.Unlock()
	if _, ok := alertIDs(c)["offline"]; !ok {
		t.Fatal("manca l'avviso offline dopo 3 ore")
	}
	if err := c.SetAlerts(0, 24); err != nil {
		t.Fatal(err)
	}
	if _, ok := alertIDs(c)["offline"]; ok {
		t.Fatal("avviso offline con soglia 0")
	}
	c.SetAlerts(2, 24)
	env.down.Store(false)
	c.SyncNow()
	waitFor(t, "NAS di nuovo raggiungibile", func() bool { _, ok := alertIDs(c)["offline"]; return !ok })
}

func TestAlertBackupOld(t *testing.T) {
	env := newTestEnv(t)
	c := env.newClient("PC1")
	setHubBackup(env, true, time.Now().Add(-30*time.Hour))
	c.refreshBackup(context.Background())
	if _, ok := alertIDs(c)["backup"]; !ok {
		t.Fatal("manca l'avviso sul backup vecchio di 30 ore")
	}
	setHubBackup(env, true, time.Now())
	c.refreshBackup(context.Background())
	if _, ok := alertIDs(c)["backup"]; ok {
		t.Fatal("avviso sul backup appena riuscito")
	}
}

func TestAlertBackupNeverOK(t *testing.T) {
	env := newTestEnv(t)
	c := env.newClient("PC1")
	setHubBackup(env, true, time.Time{})
	c.refreshBackup(context.Background())
	if _, ok := alertIDs(c)["backup"]; !ok {
		t.Fatal("backup autorizzato ma mai riuscito: manca l'avviso")
	}
}

func TestAlertBackupDisabledOnHub(t *testing.T) {
	env := newTestEnv(t)
	c := env.newClient("PC1")
	setHubBackup(env, false, time.Time{})
	c.refreshBackup(context.Background())
	if _, ok := alertIDs(c)["backup"]; ok {
		t.Fatal("avviso sul backup con backup non configurato")
	}
}

func TestBackupInfoOldHub(t *testing.T) {
	env := newTestEnv(t)
	env.notFound = "/api/v1/backup"
	c := env.newClient("PC1")
	setHubBackup(env, true, time.Time{})
	before := len(c.log.Lines())
	c.refreshBackup(context.Background())
	c.mu.Lock()
	bi := c.backup
	c.mu.Unlock()
	if bi != nil {
		t.Fatalf("hub vecchio: stato del backup %+v", bi)
	}
	if _, ok := alertIDs(c)["backup"]; ok {
		t.Fatal("avviso sul backup con hub vecchio")
	}
	for _, l := range c.log.Lines()[before:] {
		if strings.Contains(l, "backup") {
			t.Fatalf("riga nel registro per un hub vecchio: %s", l)
		}
	}
}

func TestAlertBackupKeptWhenOffline(t *testing.T) {
	env := newTestEnv(t)
	c := env.newClient("PC1")
	setHubBackup(env, true, time.Now().Add(-30*time.Hour))
	c.refreshBackup(context.Background())
	env.down.Store(true)
	c.refreshBackup(context.Background())
	if _, ok := alertIDs(c)["backup"]; !ok {
		t.Fatal("avviso sul backup sparito con il NAS irraggiungibile")
	}
}

func TestAlertEvents(t *testing.T) {
	c := newBareClient(t)
	c.mu.Lock()
	c.massDel, c.massRecvDel, c.revoked, c.conflicts = 2, 1, true, []string{"a (conflitto).txt"}
	c.mu.Unlock()
	got := alertIDs(c)
	for _, id := range []string{"massDelete", "massRecvDelete", "revoked", "conflicts-1"} {
		if _, ok := got[id]; !ok {
			t.Errorf("manca %s in %v", id, got)
		}
	}
}

func TestAlertStaleOpen(t *testing.T) {
	c := newBareClient(t)
	c.mu.Lock()
	c.st.Open = true
	c.mu.Unlock()
	if _, ok := alertIDs(c)["staleOpen"]; !ok {
		t.Fatal("manca l'avviso della cassaforte rimasta aperta")
	}
}

func TestAlertUpdate(t *testing.T) {
	c := newBareClient(t)
	c.noteUpdated("0.2.10")
	if _, ok := alertIDs(c)["update-0.2.10"]; !ok {
		t.Fatal("manca l'avviso di aggiornamento installato")
	}
	c.mu.Lock()
	c.updatedAt = time.Now().Add(-25 * time.Hour)
	c.upd = &updater{st: updateState{Error: "firma", VerifyFailed: true}}
	c.mu.Unlock()
	got := alertIDs(c)
	if _, ok := got["update-0.2.10"]; ok {
		t.Fatal("avviso di aggiornamento ancora presente dopo 25 ore")
	}
	if _, ok := got["updateVerify"]; !ok {
		t.Fatal("manca l'avviso di verifica fallita")
	}
}

func TestVerifyErrorMarked(t *testing.T) {
	_, err := verifySums([]byte("x"), []byte("non-base64!"), nil)
	var ve *verifyError
	if !errors.As(err, &ve) {
		t.Fatalf("errore di firma non marcato: %v", err)
	}
}

func TestSetAlertsValidates(t *testing.T) {
	c := newBareClient(t)
	for _, bad := range [][2]int{{-1, 24}, {2, 721}} {
		if err := c.SetAlerts(bad[0], bad[1]); err == nil {
			t.Fatalf("accettato %v", bad)
		}
	}
	if err := c.SetAlerts(6, 48); err != nil {
		t.Fatal(err)
	}
	c2, _ := newClient(c.dir)
	cfg := c2.Status()["alertCfg"].(map[string]int)
	if cfg["offlineHours"] != 6 || cfg["backupHours"] != 48 {
		t.Fatalf("soglie dopo il riavvio: %v", cfg)
	}
}

func TestAlertsAPI(t *testing.T) {
	c := newBareClient(t)
	if code, out := apiCall(t, c, "/api/alerts", map[string]any{"Offline": 6, "Backup": 48}); code != 200 {
		t.Fatalf("impostazione: %d %v", code, out)
	}
	_, st := apiCall(t, c, "/api/status", nil)
	cfg := st["alertCfg"].(map[string]any)
	if cfg["offlineHours"] != 6.0 || cfg["backupHours"] != 48.0 {
		t.Fatalf("soglie nello stato: %v", cfg)
	}
	if code, _ := apiCall(t, c, "/api/alerts", map[string]any{"Offline": -1, "Backup": 24}); code != 400 {
		t.Fatalf("soglia negativa accettata: %d", code)
	}
}

// Il NAS risponde ma rifiuta questo client (426): non è "il NAS non risponde".
func TestAlertIncompatibleNotOffline(t *testing.T) {
	env := newTestEnv(t)
	c := env.newClient("PC1")
	env.status.Store(426)
	c.SyncNow()
	waitFor(t, "rifiuto del NAS", func() bool { _, ok := alertIDs(c)["incompatible"]; return ok })
	c.mu.Lock()
	since := c.offlineSince
	c.offlineSince = time.Time{}
	c.mu.Unlock()
	if !since.IsZero() {
		t.Fatal("conteggio 'NAS non risponde' avviato con il NAS che risponde 426")
	}
	env.status.Store(0)
	c.SyncNow()
	waitFor(t, "avviso incompatibile risolto", func() bool { _, ok := alertIDs(c)["incompatible"]; return !ok })
}

// Un errore di rete non dice nulla sulla firma: l'avviso "firma non valida" resta.
func TestVerifyFailedStickyOnNetworkError(t *testing.T) {
	u, f, _ := newTestUpdater(t, "0.2.0")
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	f.release(t, other, "v0.2.1", "0.2.1", "0.2.1")
	u.check(t.Context())
	if !u.state().VerifyFailed {
		t.Fatal("firma non valida non segnalata")
	}
	f.srv.Close()
	u.check(t.Context())
	if !u.state().VerifyFailed {
		t.Fatal("avviso di firma azzerato da un errore di rete")
	}
}

func TestVerifyFailedResetOnSuccess(t *testing.T) {
	u, f, priv := newTestUpdater(t, "0.2.0")
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	f.release(t, other, "v0.2.1", "0.2.1", "0.2.1")
	u.check(t.Context())
	f.release(t, priv, "v0.2.1", "0.2.1", "0.2.1")
	if err := u.check(t.Context()); err != nil {
		t.Fatal(err)
	}
	if u.state().VerifyFailed {
		t.Fatal("avviso di firma rimasto dopo un controllo riuscito")
	}
}

// Release appena creata, file di firma non ancora caricati: non è un attacco, niente avviso.
func TestMissingSumsNotVerifyFailure(t *testing.T) {
	u, f, priv := newTestUpdater(t, "0.2.0")
	f.release(t, priv, "v0.2.1", "0.2.1", "0.2.1")
	delete(f.files, "SHA256SUMS.sig")
	if err := u.check(t.Context()); err == nil {
		t.Fatal("release senza firma accettata")
	}
	if u.state().VerifyFailed {
		t.Fatal("file di firma mancanti trattati come firma non valida")
	}
}

func TestBackupErrorSanitized(t *testing.T) {
	raw := `Put "https://www.googleapis.com/upload/drive/v3/files?uploadType=resumable&upload_id=SEGRETO123": dial tcp: lookup www.googleapis.com: no such host` + "\n{\n  \"error\": 1\n}"
	got := cleanBackupErr(raw)
	if strings.Contains(got, "SEGRETO123") || strings.Contains(got, "\n") || strings.Contains(got, "upload_id") {
		t.Fatalf("errore non ripulito: %q", got)
	}
	if !strings.Contains(got, "www.googleapis.com") || !strings.Contains(got, "no such host") {
		t.Fatalf("errore ripulito troppo: %q", got)
	}
	if long := cleanBackupErr(strings.Repeat("x", 500)); len([]rune(long)) > 201 {
		t.Fatalf("errore non troncato: %d", len(long))
	}
}

func setHubVer(env *testEnv, v string) { env.hubVer.Store(&v) }

func hubOutdatedAlert(c *Client) (alert, bool) {
	for id, a := range alertIDs(c) {
		if strings.HasPrefix(id, "hubOutdated") {
			return a, true
		}
	}
	return alert{}, false
}

// Il NAS ha una versione più vecchia del computer: avviso con l'istruzione per aggiornarlo.
func TestAlertHubOutdated(t *testing.T) {
	env := newTestEnv(t)
	c := env.newClient("PC1")
	setHubVer(env, "0.2.1")
	c.SyncNow()
	waitFor(t, "avviso NAS da aggiornare", func() bool { _, ok := alertIDs(c)["hubOutdated-"+Version]; return ok })
	a := alertIDs(c)["hubOutdated-"+Version]
	if !strings.Contains(a.Text, "0.2.1") || !strings.Contains(a.Text, "Stop") || a.Level != "info" {
		t.Fatalf("avviso: %+v", a)
	}
	setHubVer(env, Version)
	c.SyncNow()
	waitFor(t, "avviso risolto", func() bool { _, ok := hubOutdatedAlert(c); return !ok })
}

// Hub precedente alla 0.2.12 (nessuna intestazione) o intestazione non valida: nessun avviso.
func TestAlertHubOutdatedIgnoresOldOrBadHeader(t *testing.T) {
	env := newTestEnv(t)
	c := env.newClient("PC1")
	for _, v := range []string{"-", "abc", "0.2.1<script>", ""} {
		setHubVer(env, v)
		c.SyncNow()
		time.Sleep(300 * time.Millisecond)
		if a, ok := hubOutdatedAlert(c); ok {
			t.Fatalf("intestazione %q: avviso inatteso %+v", v, a)
		}
	}
}
