package main

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"encluca/internal/rcrypt"
)

const (
	browsePw      = "una password lunga e sicura"
	browseRel     = "Documenti/nota segreta.txt"
	browseContent = "testo riservato"
)

// insecureClient accetta il certificato autofirmato dell'hub (solo nei test).
func insecureClient() *http.Client {
	return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
}

// adminClient restituisce un client HTTP già autenticato come amministratore dell'hub.
func adminClient(t *testing.T, env *testEnv) *http.Client {
	t.Helper()
	pw, err := os.ReadFile(filepath.Join(env.hub.dir, "admin-password.txt"))
	if err != nil {
		t.Fatal(err)
	}
	c := insecureClient()
	c.Jar, _ = cookiejar.New(nil)
	body, _ := json.Marshal(map[string]string{"Password": strings.TrimSpace(string(pw))})
	req, _ := http.NewRequest(http.MethodPost, env.srv.URL+"/admin/api/login", bytes.NewReader(body))
	req.Header.Set("X-Encluca", "1")
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("login amministratore: %v %v", err, resp)
	}
	resp.Body.Close()
	return c
}

func get(t *testing.T, c *http.Client, url string) (int, []byte) {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func setupBrowse(t *testing.T) (*testEnv, *http.Client) {
	env := newTestEnv(t)
	pc := env.newClient("PC1")
	if err := pc.CreateVault(browsePw); err != nil {
		t.Fatal(err)
	}
	writeWork(t, pc, browseRel, browseContent)
	waitFor(t, "upload", func() bool { return pendingCount(pc) == 0 && env.hubFiles() == 1 })
	return env, adminClient(t, env)
}

func TestBrowseAPI(t *testing.T) {
	env, admin := setupBrowse(t)
	u := env.srv.URL + "/admin/api/vault/"

	// senza accesso amministratore: rifiutato
	if code, _ := get(t, insecureClient(), u+"list"); code != http.StatusUnauthorized {
		t.Fatalf("elenco senza login: %d", code)
	}
	// spenta di default
	if code, _ := get(t, admin, u+"list"); code != http.StatusForbidden {
		t.Fatalf("elenco con funzione spenta: %d", code)
	}
	req, _ := http.NewRequest(http.MethodPost, env.srv.URL+"/admin/api/browse", strings.NewReader(`{"Enabled":true}`))
	req.Header.Set("X-Encluca", "1")
	if resp, err := admin.Do(req); err != nil || resp.StatusCode != 200 {
		t.Fatalf("attivazione: %v %v", err, resp)
	}

	code, b := get(t, admin, u+"list")
	var items []browseItem
	json.Unmarshal(b, &items)
	if code != 200 || len(items) != 1 {
		t.Fatalf("elenco: %d %s", code, b)
	}
	if strings.Contains(string(b), "Documenti") || strings.Contains(string(b), "segreta") {
		t.Fatal("l'elenco contiene nomi in chiaro")
	}
	if items[0].Size != int64(len(browseContent)) {
		t.Fatalf("dimensione in chiaro %d, attesa %d", items[0].Size, len(browseContent))
	}

	_, mb := get(t, admin, u+"meta")
	var meta vaultMeta
	json.Unmarshal(mb, &meta)
	code, enc := get(t, admin, u+"file?p="+items[0].P)
	if code != 200 || bytes.Contains(enc, []byte(browseContent)) {
		t.Fatalf("file: %d, contenuto in chiaro presente: %v", code, bytes.Contains(enc, []byte(browseContent)))
	}
	// ciò che riceve il browser si decifra con la sola password
	cip, err := rcrypt.New(browsePw, meta.Salt)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := cip.DecryptStream(&out, bytes.NewReader(enc)); err != nil || out.String() != browseContent {
		t.Fatalf("decifratura: %v %q", err, out.String())
	}
	if name, _ := cip.DecryptPath(items[0].P); name != browseRel {
		t.Fatalf("nome decifrato %q", name)
	}
	if code, _ := get(t, admin, u+"file?p=../index.json"); code != http.StatusBadRequest {
		t.Fatalf("percorso esterno accettato: %d", code)
	}
}

// Il modulo WebAssembly, compilato dal sorgente ed eseguito in Node, decifra ciò che fornisce l'hub.
func TestBrowseWasm(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node non disponibile")
	}
	env, admin := setupBrowse(t)
	req, _ := http.NewRequest(http.MethodPost, env.srv.URL+"/admin/api/browse", strings.NewReader(`{"Enabled":true}`))
	req.Header.Set("X-Encluca", "1")
	admin.Do(req)
	u := env.srv.URL + "/admin/api/vault/"
	_, mb := get(t, admin, u+"meta")
	var meta vaultMeta
	json.Unmarshal(mb, &meta)
	_, lb := get(t, admin, u+"list")
	var items []browseItem
	json.Unmarshal(lb, &items)
	_, enc := get(t, admin, u+"file?p="+items[0].P)

	dir := t.TempDir()
	wasm := filepath.Join(dir, "vault.wasm")
	build := exec.Command("go", "build", "-o", wasm, "./cmd/vaultwasm")
	build.Env = append(os.Environ(), "GOOS=js", "GOARCH=wasm")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("compilazione wasm: %v\n%s", err, out)
	}
	fx, _ := json.Marshal(map[string]string{"password": browsePw, "salt": meta.Salt, "check": meta.Check,
		"enc": items[0].P, "file": base64.StdEncoding.EncodeToString(enc)})
	os.WriteFile(filepath.Join(dir, "fx.json"), fx, 0o600)
	script := `if(!globalThis.crypto) globalThis.crypto = require('crypto').webcrypto;
require(process.argv[2]);
const fs = require('fs'); const fx = JSON.parse(fs.readFileSync(process.argv[4]));
const go = new Go();
WebAssembly.instantiate(fs.readFileSync(process.argv[3]), go.importObject).then(r => {
  go.run(r.instance);
  const v = globalThis.enclucaVault;
  const bad = v.unlock('password sbagliata', fx.salt, fx.check);
  const locked = v.name(fx.enc);
  const ok = v.unlock(fx.password, fx.salt, fx.check);
  const name = v.name(fx.enc);
  const out = v.file(new Uint8Array(Buffer.from(fx.file, 'base64')));
  v.lock();
  const after = v.name(fx.enc);
  console.log(JSON.stringify({bad, locked, ok, name, content: Buffer.from(out).toString(), after}));
  process.exit(0);
});`
	os.WriteFile(filepath.Join(dir, "run.js"), []byte(script), 0o600)
	wasmExec := filepath.Join(runtime.GOROOT(), "misc", "wasm", "wasm_exec.js")
	out, err := exec.Command(node, filepath.Join(dir, "run.js"), wasmExec, wasm, filepath.Join(dir, "fx.json")).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
	var res struct {
		Bad, Ok, Name, Content string
		Locked, After          *string
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &res); err != nil {
		t.Fatalf("risposta: %s", out)
	}
	switch {
	case res.Bad != "password errata":
		t.Fatalf("password errata accettata: %q", res.Bad)
	case res.Locked != nil:
		t.Fatal("nome decifrato prima dello sblocco")
	case res.Ok != "":
		t.Fatalf("sblocco: %q", res.Ok)
	case res.Name != browseRel || res.Content != browseContent:
		t.Fatalf("decifratura nel browser: %q %q", res.Name, res.Content)
	case res.After != nil:
		t.Fatal("nome decifrato dopo il blocco")
	}
}
