package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

const testPw = "una password lunga e sicura"

func newBareClient(t *testing.T) *Client {
	t.Helper()
	dir := t.TempDir()
	c, err := newClient(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	c.cfg.WorkDir = filepath.Join(dir, "Encluca - APERTA")
	return c
}

func postJSON(t *testing.T, c *http.Client, url string, body any) (int, []byte) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(b))
	req.Header.Set("X-Encluca", "1")
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.Bytes()
}

// Punto 8: il codice di abbinamento vale una volta sola e non è esso stesso un token.
func TestPairCodeOneTime(t *testing.T) {
	env := newTestEnv(t)
	admin := adminClient(t, env)
	code, body := postJSON(t, admin, env.srv.URL+"/admin/api/devices", map[string]string{"Name": "PC1"})
	if code != 200 {
		t.Fatalf("creazione dispositivo: %d %s", code, body)
	}
	var res struct{ Code string }
	json.Unmarshal(body, &res)
	if !strings.HasPrefix(res.Code, "EL2-") {
		t.Fatalf("codice inatteso: %q", res.Code)
	}
	pc, _ := parsePairCode(res.Code)

	// prima dell'uso il dispositivo non è attivo e la chiave non funziona come token
	req, _ := http.NewRequest(http.MethodGet, env.srv.URL+"/api/v1/changes", nil)
	req.Header.Set("Authorization", "Bearer "+pc.Key)
	if resp, err := insecureClient().Do(req); err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("la chiave di abbinamento è stata accettata come token: %v %v", err, resp)
	}

	c1 := newBareClient(t)
	if err := c1.Pair(res.Code, "", "PC1"); err != nil {
		t.Fatalf("primo abbinamento: %v", err)
	}
	if c1.cfg.Token == "" || c1.cfg.Token == pc.Key {
		t.Fatal("il client non ha ricevuto un token distinto dalla chiave")
	}
	var ch map[string]any
	if err := c1.getJSON(env.ctx, "/api/v1/changes", nil, &ch); err != nil {
		t.Fatalf("il token ottenuto non funziona: %v", err)
	}
	c2 := newBareClient(t)
	if err := c2.Pair(res.Code, "", "intruso"); err == nil || !strings.Contains(err.Error(), "già usato") {
		t.Fatalf("lo stesso codice è stato accettato due volte: %v", err)
	}
	if c2.cfg.Token != "" || c2.cfg.HubURL != "" {
		t.Fatal("abbinamento fallito ma configurazione cambiata")
	}

	// codice scaduto
	expired := env.pairCode("vecchio")
	env.hub.mu.Lock()
	env.hub.cfg.Devices[len(env.hub.cfg.Devices)-1].PairExp = time.Now().Add(-time.Minute)
	env.hub.mu.Unlock()
	if err := newBareClient(t).Pair(expired, "", "x"); err == nil {
		t.Fatal("codice scaduto accettato")
	}

	// il vecchio formato EL1 continua a funzionare
	if err := newBareClient(t).Pair(env.pairCodeEL1("legacy"), "", "legacy"); err != nil {
		t.Fatalf("codice EL1: %v", err)
	}
}

// Punto 2: abbinarsi a un altro NAS non butta le modifiche mai arrivate al precedente.
func TestPairNewNASKeepsPending(t *testing.T) {
	env1 := newTestEnv(t)
	pc := env1.newClient("PC1")
	if err := pc.CreateVault(testPw); err != nil {
		t.Fatal(err)
	}
	env1.down.Store(true)
	writeWork(t, pc, "solo qui.txt", "mai arrivato al NAS")
	waitFor(t, "modifica in coda", func() bool { return pendingCount(pc) == 1 })
	if err := pc.Close(); err != nil {
		t.Fatal(err)
	}

	env2 := newTestEnv(t)
	if err := pc.Pair(env2.pairCode("PC1"), "", "PC1"); err != nil {
		t.Fatal(err)
	}
	arch, _ := filepath.Glob(filepath.Join(pc.dir, "nas-precedente-*"))
	if len(arch) != 1 {
		t.Fatalf("archivio del NAS precedente non creato: %v", arch)
	}
	var st clientState
	if err := loadJSON(filepath.Join(arch[0], "state.json"), &st); err != nil {
		t.Fatal(err)
	}
	found := 0
	for enc, f := range st.Files {
		if f.Pending == "put" {
			if _, err := os.Stat(filepath.Join(arch[0], "mirror", filepath.FromSlash(enc))); err != nil {
				t.Fatalf("file cifrato in coda non archiviato: %v", err)
			}
			found++
		}
	}
	if found != 1 {
		t.Fatalf("modifiche in coda archiviate: %d, attese 1", found)
	}
	if _, err := os.Stat(filepath.Join(arch[0], "vault-meta.json")); err != nil {
		t.Fatal("metadati della vecchia cassaforte non archiviati (servono per decifrare)")
	}
	if pendingCount(pc) != 0 || pc.st.Seq != 0 {
		t.Fatal("lo stato del nuovo NAS non riparte da zero")
	}
	if _, err := os.Stat(pc.metaCache()); err == nil {
		t.Fatal("restano i metadati della vecchia cassaforte, ma il nuovo NAS non ne ha")
	}
}

// Punto 3a: i file cancellati finiscono nel cestino dell'hub e si possono ripristinare.
func TestHubTrashRestore(t *testing.T) {
	env := newTestEnv(t)
	pc1 := env.newClient("PC1")
	if err := pc1.CreateVault(testPw); err != nil {
		t.Fatal(err)
	}
	writeWork(t, pc1, "importante.txt", "da non perdere")
	waitFor(t, "upload", func() bool { return pendingCount(pc1) == 0 && env.hubFiles() == 1 })
	pc2 := env.newClient("PC2")
	if err := pc2.Open(testPw); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "file su PC2", func() bool { return readWork(pc2, "importante.txt") == "da non perdere" })

	os.Remove(filepath.Join(pc1.cfg.WorkDir, "importante.txt"))
	waitFor(t, "eliminazione su PC2", func() bool { return readWork(pc2, "importante.txt") == "<assente>" })
	env.hub.mu.Lock()
	n := len(env.hub.trash)
	env.hub.mu.Unlock()
	if n != 1 {
		t.Fatalf("elementi nel cestino: %d", n)
	}

	admin := adminClient(t, env)
	code, body := postJSON(t, admin, env.srv.URL+"/admin/api/trash/restore", map[string]int{"Hours": 24})
	if code != 200 || !strings.Contains(string(body), `"restored":1`) {
		t.Fatalf("ripristino: %d %s", code, body)
	}
	waitFor(t, "file ripristinato su PC1", func() bool { return readWork(pc1, "importante.txt") == "da non perdere" })
	waitFor(t, "file ripristinato su PC2", func() bool { return readWork(pc2, "importante.txt") == "da non perdere" })

	// la pulizia elimina definitivamente ciò che è più vecchio di 30 giorni
	os.Remove(filepath.Join(pc1.cfg.WorkDir, "importante.txt"))
	waitFor(t, "di nuovo nel cestino", func() bool {
		env.hub.mu.Lock()
		defer env.hub.mu.Unlock()
		return len(env.hub.trash) == 1
	})
	env.hub.mu.Lock()
	env.hub.trash[0].Deleted = time.Now().Add(-31 * 24 * time.Hour).UnixNano()
	file := filepath.Join(env.hub.trashDir(), env.hub.trash[0].File)
	env.hub.purgeTrashLocked()
	left := len(env.hub.trash)
	env.hub.mu.Unlock()
	if _, err := os.Stat(file); left != 0 || err == nil {
		t.Fatal("elemento scaduto non eliminato dal cestino")
	}
}

// Punto 3b: una cancellazione di massa arrivata dal NAS resta sospesa fino alla conferma.
func TestMassRecvDeleteHeld(t *testing.T) {
	env := newTestEnv(t)
	pc1 := env.newClient("PC1")
	if err := pc1.CreateVault(testPw); err != nil {
		t.Fatal(err)
	}
	const n = 30
	for i := 0; i < n; i++ {
		writeWork(t, pc1, fmt.Sprintf("f%02d.txt", i), "x")
	}
	waitFor(t, "upload", func() bool { return pendingCount(pc1) == 0 && env.hubFiles() == n })
	pc2 := env.newClient("PC2")
	if err := pc2.Open(testPw); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "file su PC2", func() bool { return len(listWork(pc2)) == n })

	for i := 0; i < n; i++ {
		os.Remove(filepath.Join(pc1.cfg.WorkDir, fmt.Sprintf("f%02d.txt", i)))
	}
	waitFor(t, "sospensione su PC1", func() bool { pc1.mu.Lock(); defer pc1.mu.Unlock(); return pc1.massDel == n })
	pc1.ConfirmDeletes()
	waitFor(t, "eliminazioni sul NAS", func() bool {
		env.hub.mu.Lock()
		defer env.hub.mu.Unlock()
		return len(env.hub.trash) == n
	})
	waitFor(t, "sospensione su PC2", func() bool { pc2.mu.Lock(); defer pc2.mu.Unlock(); return pc2.massRecvDel == n })
	time.Sleep(time.Second)
	if got := len(listWork(pc2)); got != n {
		t.Fatalf("PC2 ha eliminato senza conferma: restano %d file", got)
	}
	pc2.ConfirmRecvDeletes()
	waitFor(t, "eliminazione confermata su PC2", func() bool { return len(listWork(pc2)) == 0 })
}

// Punto 4: le API dell'interfaccia locale richiedono il token scritto nella pagina.
func TestClientUIToken(t *testing.T) {
	c := newBareClient(t)
	srv := httptest.NewServer(securityHeaders(c.uiHandler("127.0.0.1:8484")))
	defer srv.Close()
	do := func(method, path, tok string) (int, string) {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader("{}"))
		req.Host = "127.0.0.1:8484"
		req.Header.Set("X-Encluca", "1")
		if tok != "" {
			req.Header.Set("X-Encluca-Token", tok)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var b bytes.Buffer
		b.ReadFrom(resp.Body)
		return resp.StatusCode, b.String()
	}
	if code, _ := do("GET", "/api/status", ""); code != http.StatusForbidden {
		t.Fatalf("stato senza token: %d", code)
	}
	if code, _ := do("POST", "/api/close", "sbagliato"); code != http.StatusForbidden {
		t.Fatalf("comando con token errato: %d", code)
	}
	_, page := do("GET", "/", "")
	m := regexp.MustCompile(`name="encluca-token" content="([0-9a-f]{64})"`).FindStringSubmatch(page)
	if m == nil {
		t.Fatal("token non presente nella pagina")
	}
	if code, body := do("GET", "/api/status", m[1]); code != 200 || !strings.Contains(body, `"version"`) {
		t.Fatalf("stato con token: %d %s", code, body)
	}
}

// Punto 5: password nuove di almeno 14 caratteri (cassaforte e amministratore).
func TestPasswordMinLength(t *testing.T) {
	env := newTestEnv(t)
	pc := env.newClient("PC1")
	if err := pc.CreateVault("tredici-carat"); err == nil {
		t.Fatal("cassaforte creata con una password di 13 caratteri")
	}
	if err := pc.CreateVault("quattordici-ch"); err != nil {
		t.Fatalf("password di 14 caratteri rifiutata: %v", err)
	}
	admin := adminClient(t, env)
	old, _ := os.ReadFile(filepath.Join(env.hub.dir, "admin-password.txt"))
	code, _ := postJSON(t, admin, env.srv.URL+"/admin/api/password", map[string]string{"Old": strings.TrimSpace(string(old)), "New": "tredici-carat"})
	if code != http.StatusBadRequest {
		t.Fatalf("password amministratore di 13 caratteri: %d", code)
	}
}

// Punto 6: dopo 5 errori l'IP viene rallentato, anche se poi prova la password giusta;
// gli altri indirizzi non sono toccati.
func TestAdminLoginLimiter(t *testing.T) {
	env := newTestEnv(t)
	c := insecureClient()
	for i := 0; i < limiterFree; i++ {
		if code, _ := postJSON(t, c, env.srv.URL+"/admin/api/login", map[string]string{"Password": "sbagliata"}); code != http.StatusUnauthorized {
			t.Fatalf("tentativo %d: %d", i+1, code)
		}
	}
	pw, _ := os.ReadFile(filepath.Join(env.hub.dir, "admin-password.txt"))
	if code, _ := postJSON(t, c, env.srv.URL+"/admin/api/login", map[string]string{"Password": strings.TrimSpace(string(pw))}); code != http.StatusTooManyRequests {
		t.Fatalf("dopo 5 errori il login non è rallentato: %d", code)
	}
	if env.hub.limiter.blocked("192.0.2.7") != 0 {
		t.Fatal("un altro IP è stato bloccato")
	}
	// mentre lo scrypt è in corso il lock dell'hub resta libero
	env.hub.limiter.ok("127.0.0.1")
	done := make(chan struct{})
	go func() { env.hub.checkAdmin("qualcosa"); close(done) }()
	time.Sleep(10 * time.Millisecond)
	start := time.Now()
	env.hub.mu.Lock()
	held := time.Since(start)
	env.hub.mu.Unlock()
	<-done
	if held > 20*time.Millisecond {
		t.Fatal("la verifica della password trattiene il lock dell'hub")
	}
}

// Punto 7: cambiare la password amministratore chiude le altre sessioni.
func TestPasswordChangeEndsOtherSessions(t *testing.T) {
	env := newTestEnv(t)
	a, b := adminClient(t, env), adminClient(t, env)
	old, _ := os.ReadFile(filepath.Join(env.hub.dir, "admin-password.txt"))
	code, body := postJSON(t, a, env.srv.URL+"/admin/api/password", map[string]string{"Old": strings.TrimSpace(string(old)), "New": "nuova password molto lunga"})
	if code != 200 {
		t.Fatalf("cambio password: %d %s", code, body)
	}
	if code, _ := get(t, a, env.srv.URL+"/admin/api/status"); code != 200 {
		t.Fatalf("la sessione che ha cambiato la password è stata chiusa: %d", code)
	}
	if code, _ := get(t, b, env.srv.URL+"/admin/api/status"); code != http.StatusUnauthorized {
		t.Fatalf("l'altra sessione è ancora valida: %d", code)
	}
	c := insecureClient()
	c.Jar, _ = cookiejar.New(nil)
	if code, _ := postJSON(t, c, env.srv.URL+"/admin/api/login", map[string]string{"Password": "nuova password molto lunga"}); code != 200 {
		t.Fatalf("login con la nuova password: %d", code)
	}
}

// Punto 9: le GitHub Actions sono fissate a un commit (SHA), non a un tag spostabile.
func TestActionsPinnedBySHA(t *testing.T) {
	files, _ := filepath.Glob(".github/workflows/*.yml")
	if len(files) == 0 {
		t.Fatal("nessun workflow trovato")
	}
	uses := regexp.MustCompile(`(?m)^\s*-?\s*uses:\s*(\S+)`)
	pinned := regexp.MustCompile(`@[0-9a-f]{40}$`)
	for _, f := range files {
		b, _ := os.ReadFile(f)
		for _, m := range uses.FindAllStringSubmatch(string(b), -1) {
			if !pinned.MatchString(m[1]) {
				t.Errorf("%s: %s non è fissata a uno SHA", f, m[1])
			}
		}
	}
}
