//go:build linux

package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Prova completa con il programma vero: la 0.2.0 trova la 0.2.1 firmata, la
// installa (cassaforte chiusa) ed esce con il codice 75 per farsi riavviare.
func TestUpdateEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("prova lunga: compila due volte il programma")
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	tmp := t.TempDir()
	build := func(ver, out string) {
		cmd := exec.Command("go", "build", "-o", out, "-ldflags",
			"-X main.Version="+ver+" -X main.releasePubKeyHex="+hex.EncodeToString(pub), ".")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", ver, err, b)
		}
	}
	exe := filepath.Join(tmp, "bin", "encluca")
	os.MkdirAll(filepath.Dir(exe), 0o755)
	build("0.2.0", exe)
	newBin := filepath.Join(tmp, "new")
	build("0.2.1", newBin)
	binData, _ := os.ReadFile(newBin)

	arch := makeTarGz(t, map[string][]byte{"encluca-0.2.1-linux-amd64/encluca": binData}, nil)
	name := assetName("0.2.1", "linux", "amd64")
	sum := sha256.Sum256(arch)
	sums := hex.EncodeToString(sum[:]) + "  " + name + "\n"
	files := map[string][]byte{name: arch, "SHA256SUMS": []byte(sums), "SHA256SUMS.sig": signed(t, priv, sums)}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/latest" {
			var as []map[string]string
			for n := range files {
				as = append(as, map[string]string{"name": n, "browser_download_url": srv.URL + "/dl/" + n})
			}
			json.NewEncoder(w).Encode(map[string]any{"tag_name": "v0.2.1", "assets": as})
			return
		}
		if b, ok := files[strings.TrimPrefix(r.URL.Path, "/dl/")]; ok {
			w.Write(b)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	data := filepath.Join(tmp, "data")
	cmd := exec.Command(exe, "client", "--data", data, "--ui", addr)
	cmd.Env = append(os.Environ(), "ENCLUCA_UPDATE_API="+srv.URL+"/latest", "HOME="+tmp)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != updateExitCode {
			t.Fatalf("uscita: %v (atteso codice %d)", err, updateExitCode)
		}
	case <-time.After(90 * time.Second):
		cmd.Process.Kill()
		t.Fatal("il client non si è riavviato entro 90 s")
	}
	out, err := exec.Command(exe, "version").Output()
	if err != nil || strings.TrimSpace(string(out)) != "Encluca 0.2.1" {
		t.Fatalf("versione dopo l'aggiornamento: %q %v", out, err)
	}
	logb, _ := os.ReadFile(filepath.Join(data, "encluca.log"))
	if !strings.Contains(string(logb), "Installo l'aggiornamento 0.2.1") {
		t.Fatalf("registro:\n%s", logb)
	}
	if _, err := os.Stat(filepath.Join(data, "update-installed.json")); err != nil {
		t.Fatal("update-installed.json mancante")
	}
	// al riavvio la nuova versione registra l'aggiornamento e pulisce
	cmd = exec.Command(exe, "client", "--data", data, "--ui", addr)
	cmd.Env = append(os.Environ(), "ENCLUCA_UPDATE_API="+srv.URL+"/latest", "HOME="+tmp)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	deadline := time.Now().Add(20 * time.Second)
	for {
		logb, _ = os.ReadFile(filepath.Join(data, "encluca.log"))
		if strings.Contains(string(logb), "Aggiornamento installato: 0.2.0 → 0.2.1") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("registro dopo il riavvio:\n%s", logb)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(data, "update-installed.json")); err == nil {
		t.Fatal("update-installed.json non cancellato")
	}
}
