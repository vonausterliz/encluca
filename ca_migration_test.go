package main

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func setCAActive(env *testEnv, on bool) {
	env.hub.mu.Lock()
	env.hub.cfg.CA.Active = on
	env.hub.mu.Unlock()
}

// newConn obbliga il client ad aprire una nuova connessione TLS (quelle già aperte restano valide).
func newConn(c *Client) { c.hc.CloseIdleConnections() }

// syncsFile scrive un file sul client e aspetta che arrivi sul NAS.
func syncsFile(t *testing.T, env *testEnv, c *Client, rel, body string) {
	t.Helper()
	writeWork(t, c, rel, body)
	waitFor(t, "arrivo sul NAS di "+rel, func() bool {
		e := hubEntry(env, c, rel)
		return pendingCount(c) == 0 && e != nil && !e.Del
	})
}

func deviceCAFP(env *testEnv, name string) string {
	env.hub.mu.Lock()
	defer env.hub.mu.Unlock()
	for _, d := range env.hub.cfg.Devices {
		if d.Name == name {
			return d.CAFP
		}
	}
	return "?"
}

func clientCA(c *Client) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cfg.CAFingerprint
}

func TestClientLearnsCA(t *testing.T) {
	env, a, _ := twoOpen(t)
	syncsFile(t, env, a, "a.txt", "uno")
	waitFor(t, "CA ricevuta dal client", func() bool { return clientCA(a) == env.hub.caFingerprint })
	waitFor(t, "CA dichiarata all'hub", func() bool { return deviceCAFP(env, "A") == env.hub.caFingerprint })
	// e resta salvata
	var cfg clientConfig
	if err := loadJSON(a.cfgPath(), &cfg); err != nil || cfg.CAFingerprint != env.hub.caFingerprint {
		t.Fatalf("CA non salvata nella configurazione: %v %q", err, cfg.CAFingerprint)
	}
}

func TestClientSurvivesActivation(t *testing.T) {
	env, a, b := twoOpen(t)
	syncsFile(t, env, a, "prima.txt", "prima")
	waitFor(t, "CA ricevuta", func() bool {
		return clientCA(a) == env.hub.caFingerprint && clientCA(b) == env.hub.caFingerprint
	})
	setCAActive(env, true)
	newConn(a)
	newConn(b)
	syncsFile(t, env, a, "dopo.txt", "dopo l'attivazione")
	waitFor(t, "file su B dopo l'attivazione", func() bool { return readWork(b, "dopo.txt") == "dopo l'attivazione" })
}

func TestClientAcceptsReissuedLeaf(t *testing.T) {
	env, a, _ := twoOpen(t)
	syncsFile(t, env, a, "prima.txt", "prima")
	waitFor(t, "CA ricevuta", func() bool { return clientCA(a) == env.hub.caFingerprint })
	setCAActive(env, true)
	newConn(a)
	syncsFile(t, env, a, "uno.txt", "uno")
	before := fpOf(served(t, env)[0])
	env.hub.mu.Lock()
	env.hub.addHostLocked("10.9.9.9") // rigenera il certificato del server
	env.hub.mu.Unlock()
	if fpOf(served(t, env)[0]) == before {
		t.Fatal("il certificato del server non è stato rigenerato")
	}
	newConn(a)
	syncsFile(t, env, a, "due.txt", "due")
}

func TestDeactivateFallsBack(t *testing.T) {
	env, a, _ := twoOpen(t)
	syncsFile(t, env, a, "prima.txt", "prima")
	waitFor(t, "CA ricevuta", func() bool { return clientCA(a) == env.hub.caFingerprint })
	setCAActive(env, true)
	newConn(a)
	syncsFile(t, env, a, "attivo.txt", "attivo")
	setCAActive(env, false)
	newConn(a)
	syncsFile(t, env, a, "tornato.txt", "tornato indietro")
}

func TestClientRejectsOtherCA(t *testing.T) {
	env, a, _ := twoOpen(t)
	syncsFile(t, env, a, "prima.txt", "prima")
	waitFor(t, "CA ricevuta", func() bool { return clientCA(a) == env.hub.caFingerprint })
	setCAActive(env, true)
	var hits atomic.Int32
	count := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) })
	// (a) un certificato autofirmato qualunque
	selfSigned := httptest.NewTLSServer(count)
	defer selfSigned.Close()
	// (b) un altro hub, con la sua CA, che presenta foglia e CA in catena
	other, err := newHub(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	other.cfg.CA.Active = true
	otherSrv := httptest.NewUnstartedServer(count)
	startHubTLS(otherSrv, other)
	defer otherSrv.Close()
	for _, u := range []string{selfSigned.URL, otherSrv.URL} {
		a.mu.Lock()
		a.cfg.HubURL = u
		a.mu.Unlock()
		newConn(a)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		resp, err := a.req(ctx, http.MethodGet, "/api/v1/ping", nil, nil, -1)
		cancel()
		if err == nil {
			resp.Body.Close()
			t.Fatalf("il client ha accettato un server estraneo (%s)", u)
		}
	}
	time.Sleep(200 * time.Millisecond)
	if hits.Load() != 0 {
		t.Fatalf("al server estraneo sono arrivate %d richieste", hits.Load())
	}
}

func TestActivationRequiresReadyDevices(t *testing.T) {
	env := newTestEnv(t)
	env.deviceToken("PC vecchio") // abbinato, ma non ha mai dichiarato la CA
	env.pairCode("PC in attesa")  // solo in attesa di abbinamento: non conta
	admin := adminClient(t, env)
	code, body := postJSON(t, admin, env.srv.URL+"/admin/api/ca/activate", map[string]bool{"Active": true})
	var res struct {
		NotReady []string `json:"notReady"`
	}
	json.Unmarshal(body, &res)
	if code != http.StatusConflict || len(res.NotReady) != 1 || res.NotReady[0] != "PC vecchio" {
		t.Fatalf("attivazione con un PC non pronto: %d %s", code, body)
	}
	env.hub.mu.Lock()
	active := env.hub.cfg.CA.Active
	env.hub.mu.Unlock()
	if active {
		t.Fatal("attivato nonostante il rifiuto")
	}
	if code, body := postJSON(t, admin, env.srv.URL+"/admin/api/ca/activate", map[string]bool{"Active": true, "Force": true}); code != 200 {
		t.Fatalf("attivazione forzata: %d %s", code, body)
	}
	env.hub.mu.Lock()
	active = env.hub.cfg.CA.Active
	env.hub.mu.Unlock()
	if !active {
		t.Fatal("non attivato con la conferma")
	}
	// la sessione amministratore è su una connessione già aperta: per tornare indietro serve una nuova
	admin.CloseIdleConnections()
	if code, body := postJSON(t, admin, env.srv.URL+"/admin/api/ca/activate", map[string]bool{"Active": false}); code != 200 {
		t.Fatalf("ritorno indietro: %d %s", code, body)
	}
}

func TestPairWithActiveCA(t *testing.T) {
	env := newTestEnv(t)
	setCAActive(env, true)
	admin := adminClient(t, env)
	code, body := postJSON(t, admin, env.srv.URL+"/admin/api/devices", map[string]string{"Name": "PC nuovo", "HubURL": env.srv.URL})
	var res struct{ Code string }
	json.Unmarshal(body, &res)
	if code != 200 || res.Code == "" {
		t.Fatalf("codice di abbinamento: %d %s", code, body)
	}
	dir := t.TempDir()
	c, err := newClient(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	c.cfg.WorkDir = filepath.Join(dir, "w")
	if err := c.Pair(res.Code, "", "PC nuovo"); err != nil {
		t.Fatalf("abbinamento con il nuovo certificato attivo: %v", err)
	}
	if clientCA(c) != env.hub.caFingerprint {
		t.Fatal("il codice di abbinamento non ha portato la CA")
	}
	c.run(env.ctx)
	if err := c.CreateVault(dirPw); err != nil {
		t.Fatal(err)
	}
	syncsFile(t, env, c, "x.txt", "ok")
}

func TestCAHostsAPI(t *testing.T) {
	env := newTestEnv(t)
	admin := adminClient(t, env)
	if code, body := postJSON(t, admin, env.srv.URL+"/admin/api/ca/hosts", map[string][]string{"Hosts": {"192.168.5.124", "NAS.local"}}); code != 200 {
		t.Fatalf("hosts: %d %s", code, body)
	}
	env.hub.mu.Lock()
	got := strings.Join(env.hub.cfg.CA.Hosts, ",")
	env.hub.mu.Unlock()
	if got != "192.168.5.124,nas.local" {
		t.Fatalf("elenco salvato: %q", got)
	}
	code, body := postJSON(t, admin, env.srv.URL+"/admin/api/ca/hosts", map[string][]string{"Hosts": {"192.168.5.124", "example.com"}})
	if code != 400 || !strings.Contains(string(body), "example.com") {
		t.Fatalf("indirizzo non ammesso: %d %s", code, body)
	}
}

func TestCADownload(t *testing.T) {
	env := newTestEnv(t)
	resp, err := insecureClient().Get(env.srv.URL + "/encluca-ca.crt")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	blk, _ := pem.Decode(data)
	if resp.StatusCode != 200 || blk == nil || !strings.Contains(resp.Header.Get("Content-Disposition"), "encluca-ca.crt") {
		t.Fatalf("scaricamento: %d %q", resp.StatusCode, resp.Header.Get("Content-Disposition"))
	}
	cert, err := x509.ParseCertificate(blk.Bytes)
	if err != nil || fpOf(cert) != env.hub.caFingerprint {
		t.Fatalf("certificato scaricato: %v", err)
	}
}
