package main

import (
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// deviceToken registra un dispositivo già attivo sull'hub e ne restituisce il token.
func (env *testEnv) deviceToken(name string) string {
	tok := randHex(32)
	env.hub.mu.Lock()
	env.hub.cfg.Devices = append(env.hub.cfg.Devices, &Device{ID: randHex(6), Name: name, TokenHash: sha256Hex([]byte(tok))})
	env.hub.mu.Unlock()
	return tok
}

// apiCall chiama l'API dei dispositivi con il protocollo indicato ("" = nessuna intestazione).
func (env *testEnv) apiCall(t *testing.T, method, path, tok, proto string, body io.Reader) (int, string) {
	t.Helper()
	r, _ := http.NewRequest(method, env.srv.URL+path, body)
	r.Header.Set("Authorization", "Bearer "+tok)
	if proto != "" {
		r.Header.Set("X-Encluca-Proto", proto)
	}
	hc := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	resp, err := hc.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestHubRejectsOldProtocol(t *testing.T) {
	env := newTestEnv(t)
	tok := env.deviceToken("vecchio")
	for _, p := range []string{"", "1", "abc"} {
		code, body := env.apiCall(t, http.MethodGet, "/api/v1/changes?since=0", tok, p, nil)
		if code != http.StatusUpgradeRequired || !strings.Contains(body, "0.2.2") {
			t.Fatalf("proto %q: %d %s", p, code, body)
		}
	}
	if code, body := env.apiCall(t, http.MethodGet, "/api/v1/changes?since=0", tok, "2", nil); code != http.StatusOK {
		t.Fatalf("proto 2: %d %s", code, body)
	}
}

func TestClientSendsProtoAndVersion(t *testing.T) {
	env := newTestEnv(t)
	pc := env.newClient("PC1")
	if err := pc.CreateVault("una password lunga e sicura"); err != nil {
		t.Fatal(err)
	}
	writeWork(t, pc, "a.txt", "ciao")
	waitFor(t, "upload", func() bool { return pendingCount(pc) == 0 && env.hubFiles() == 1 })
	env.hub.mu.Lock()
	defer env.hub.mu.Unlock()
	for _, d := range env.hub.cfg.Devices {
		if d.Name == "PC1" && d.Version != Version {
			t.Fatalf("versione registrata %q, voglio %q", d.Version, Version)
		}
	}
}

func TestClientShowsUpgradeError(t *testing.T) {
	env := newTestEnv(t)
	inner := env.hub.handler()
	old := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/changes" {
			writeErr(w, http.StatusUpgradeRequired, "NAS nuovo: aggiorna encluca")
			return
		}
		inner.ServeHTTP(w, r)
	}))
	old.TLS = &tls.Config{Certificates: []tls.Certificate{env.hub.cert}}
	old.StartTLS()
	defer old.Close()
	env.srv.Close()
	env.srv = old
	dir := t.TempDir()
	c, err := newClient(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	c.cfg.WorkDir = filepath.Join(dir, "w")
	if err := c.Pair(env.pairCode("PC"), "", "PC"); err != nil {
		t.Fatal(err)
	}
	c.run(env.ctx)
	waitFor(t, "errore di aggiornamento", func() bool {
		s, _ := c.Status()["lastError"].(string)
		return strings.Contains(s, "aggiorna")
	})
	time.Sleep(300 * time.Millisecond)
	if c.Status()["revoked"] == true {
		t.Fatal("il client non deve risultare revocato")
	}
}
