package main

import (
	"context"
	"net/http"
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
	resp, err := env.srv.Client().Get(env.srv.URL + "/api/v1/backup")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("senza token del dispositivo: %d", resp.StatusCode)
	}
}
