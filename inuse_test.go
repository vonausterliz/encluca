package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// holdOpen avvia un processo figlio ("sleep") che tiene aperto il file come standard input.
func holdOpen(t *testing.T, path string) *exec.Cmd {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("serve un processo che tenga aperto un file: solo linux/darwin")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sleep", "60")
	cmd.Stdin = f
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	return cmd
}

func release(cmd *exec.Cmd) { cmd.Process.Kill(); cmd.Wait() }

func hasInUse(list []inUseFile, path string) *inUseFile {
	for i := range list {
		if list[i].Path == path {
			return &list[i]
		}
	}
	return nil
}

func TestInUseLockFile(t *testing.T) {
	work := t.TempDir()
	for _, n := range []string{".~lock.relazione.odt#", "~$cumento.docx", ".note.txt.swp"} {
		os.WriteFile(filepath.Join(work, n), []byte("x"), 0o600)
	}
	os.MkdirAll(filepath.Join(work, "sub"), 0o700)
	os.WriteFile(filepath.Join(work, "sub", ".~lock.b.ods#"), []byte("x"), 0o600)
	got := lockFiles(work)
	for _, want := range []string{"relazione.odt", "~$cumento.docx", "note.txt", "sub/b.ods"} {
		f := hasInUse(got, want)
		if f == nil || !f.Lock {
			t.Errorf("manca %q (Lock) in %+v", want, got)
		}
	}
	if len(got) != 4 {
		t.Errorf("voci in più: %+v", got)
	}
}

func TestInUseIgnoresIndexers(t *testing.T) {
	for _, p := range []string{"tracker-extract-3", "tracker-miner-fs-3", "mdworker_shared", "mds_stores", "baloo_file"} {
		if !ignoredProc(p) {
			t.Errorf("%s non ignorato", p)
		}
	}
	if ignoredProc("soffice.bin") || ignoredProc("vim") {
		t.Error("ignorato un programma vero")
	}
}

func TestInUseOpenByChild(t *testing.T) {
	work := t.TempDir()
	p := filepath.Join(work, "sub", "a.txt")
	os.MkdirAll(filepath.Dir(p), 0o700)
	os.WriteFile(p, []byte("x"), 0o600)
	holdOpen(t, p)
	got, err := filesInUse(work)
	if err != nil {
		t.Fatal(err)
	}
	f := hasInUse(got, "sub/a.txt")
	if f == nil || f.Proc != "sleep" || f.Lock {
		t.Fatalf("file aperto non rilevato: %+v", got)
	}
}

func TestInUseIgnoresSelf(t *testing.T) {
	work := t.TempDir()
	p := filepath.Join(work, "a.txt")
	os.WriteFile(p, []byte("x"), 0o600)
	f, _ := os.Open(p)
	defer f.Close()
	got, err := filesInUse(work)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && len(got) != 0 {
		t.Fatalf("il servizio stesso risulta come programma: %+v", got)
	}
}

func TestCloseBlockedByOpenFile(t *testing.T) {
	_, c := openClient(t)
	writeWork(t, c, "a.txt", "ciao")
	cmd := holdOpen(t, workPath(c, "a.txt"))
	err := c.Close()
	var iu *inUseError
	if !errors.As(err, &iu) || hasInUse(iu.Files, "a.txt") == nil {
		t.Fatalf("chiusura non bloccata dal file aperto: %v", err)
	}
	if !isOpen(c) || readWork(c, "a.txt") != "ciao" {
		t.Fatal("cassaforte chiusa o file cancellato nonostante il blocco")
	}
	release(cmd)
	if err := c.Close(); err != nil {
		t.Fatalf("chiusura dopo aver chiuso il file: %v", err)
	}
}

func TestAutoCloseInUseRetries(t *testing.T) {
	_, c := openClient(t)
	writeWork(t, c, "a.txt", "ciao")
	waitFor(t, "a.txt cifrato", func() bool {
		return pendingCount(c) == 0 && acStatus(t, c).CloseAt.After(time.Now().Add(29*time.Minute))
	})
	cmd := holdOpen(t, workPath(c, "a.txt"))
	t0 := time.Now()
	c.mu.Lock()
	c.lastActivity = t0
	c.mu.Unlock()
	c.autoCloseTick(t0.Add(31 * time.Minute))
	if !isOpen(c) || !strings.Contains(acStatus(t, c).Error, "a.txt") {
		t.Fatalf("chiusura automatica con file aperto (errore %q)", acStatus(t, c).Error)
	}
	release(cmd)
	c.autoCloseTick(t0.Add(31*time.Minute + 30*time.Second))
	if !isOpen(c) {
		t.Fatal("nuovo tentativo prima di 1 minuto")
	}
	c.autoCloseTick(t0.Add(32*time.Minute + 5*time.Second))
	if isOpen(c) {
		t.Fatal("non chiusa dopo che il file è stato chiuso")
	}
}

// apiCall chiama l'API locale con il token di sessione, come fa la pagina.
func apiCall(t *testing.T, c *Client, path string, body any) (int, map[string]any) {
	t.Helper()
	const tok = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	srv := httptest.NewServer(securityHeaders(c.uiHandlerToken("127.0.0.1:8484", tok)))
	defer srv.Close()
	method, rd := http.MethodGet, (*strings.Reader)(nil)
	if body != nil {
		b, _ := json.Marshal(body)
		method, rd = http.MethodPost, strings.NewReader(string(b))
	}
	var req *http.Request
	if rd != nil {
		req, _ = http.NewRequest(method, srv.URL+path, rd)
	} else {
		req, _ = http.NewRequest(method, srv.URL+path, nil)
	}
	req.Host = "127.0.0.1:8484"
	req.Header.Set("X-Encluca", "1")
	req.Header.Set("X-Encluca-Token", tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestCloseAPIInUse409(t *testing.T) {
	_, c := openClient(t)
	writeWork(t, c, "doc.odt", "x")
	writeWork(t, c, ".~lock.doc.odt#", "x")
	code, out := apiCall(t, c, "/api/close", map[string]any{})
	if code != http.StatusConflict {
		t.Fatalf("codice %d, voluto 409 (%v)", code, out)
	}
	list, _ := out["inUse"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["path"] != "doc.odt" || out["error"] == "" {
		t.Fatalf("risposta: %v", out)
	}
}
