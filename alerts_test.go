package main

import (
	"context"
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
