package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseVersion(t *testing.T) {
	ok := map[string][3]int{"v0.2.0": {0, 2, 0}, "0.10.3": {0, 10, 3}, "1.0.0": {1, 0, 0}}
	for s, want := range ok {
		v, good := parseVersion(s)
		if !good || v != want {
			t.Errorf("parseVersion(%q) = %v,%v; atteso %v,true", s, v, good, want)
		}
	}
	for _, s := range []string{"0.2", "0.2.0-rc1", "x", "", "1.-1.0", "1.+1.0", "v", "1.2.3.4", "1..3"} {
		if _, good := parseVersion(s); good {
			t.Errorf("parseVersion(%q) accettata, doveva essere rifiutata", s)
		}
	}
}

func TestNewerVersion(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.2.0", "0.1.9", true},
		{"0.1.10", "0.1.9", true},
		{"v1.0.0", "0.9.9", true},
		{"0.1.9", "0.1.9", false},
		{"0.1.8", "0.1.9", false},
		{"bad", "0.1.9", false},
		{"0.2.0", "bad", false},
	}
	for _, c := range cases {
		if got := newerVersion(c.a, c.b); got != c.want {
			t.Errorf("newerVersion(%q,%q) = %v, atteso %v", c.a, c.b, got, c.want)
		}
	}
}

func TestAssetName(t *testing.T) {
	if got := assetName("0.2.0", "linux", "amd64"); got != "encluca-0.2.0-linux-amd64.tar.gz" {
		t.Errorf("linux: %s", got)
	}
	if got := assetName("0.2.0", "windows", "amd64"); got != "encluca-0.2.0-windows-amd64.zip" {
		t.Errorf("windows: %s", got)
	}
}

func signed(t *testing.T, priv ed25519.PrivateKey, sums string) []byte {
	t.Helper()
	return []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(sums))) + "\n")
}

func TestVerifySums(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	ha, hb := strings.Repeat("a", 64), strings.Repeat("b", 64)
	sums := ha + "  encluca-0.2.0-linux-amd64.tar.gz\n" + hb + "  encluca-0.2.0-windows-amd64.zip\n"
	m, err := verifySums([]byte(sums), signed(t, priv, sums), pub)
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 2 || m["encluca-0.2.0-linux-amd64.tar.gz"] != ha || m["encluca-0.2.0-windows-amd64.zip"] != hb {
		t.Fatalf("mappa errata: %v", m)
	}
	// un byte cambiato dopo la firma
	tampered := "c" + sums[1:]
	if _, err := verifySums([]byte(tampered), signed(t, priv, sums), pub); err == nil {
		t.Error("contenuto modificato accettato")
	}
	// firma di un'altra chiave
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := verifySums([]byte(sums), signed(t, other, sums), pub); err == nil {
		t.Error("firma di un'altra chiave accettata")
	}
	// nomi pericolosi o righe malformate, anche se firmati
	for _, bad := range []string{ha + "  ../x\n", ha + "  a/b\n", ha + "  a\\b\n", "rigasenzaspazi\n", ha + "  \n", "abc  file.zip\n", strings.Repeat("z", 64) + "  file.zip\n"} {
		if _, err := verifySums([]byte(bad), signed(t, priv, bad), pub); err == nil {
			t.Errorf("riga %q accettata", bad)
		}
	}
	// firma non base64
	if _, err := verifySums([]byte(sums), []byte("!!!"), pub); err == nil {
		t.Error("firma illeggibile accettata")
	}
}

func makeTarGz(t *testing.T, entries map[string][]byte, fakeSize map[string]int64) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, data := range entries {
		size := int64(len(data))
		if s, ok := fakeSize[name]; ok {
			size = s
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: size, Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, ok := fakeSize[name]; !ok {
			tw.Write(data)
		}
	}
	if len(fakeSize) == 0 {
		tw.Close()
	}
	gz.Close()
	return buf.Bytes()
}

func makeZip(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(data)
	}
	zw.Close()
	return buf.Bytes()
}

func TestExtractBinary(t *testing.T) {
	dir := t.TempDir()
	arch := filepath.Join(dir, "a.tar.gz")
	out := filepath.Join(dir, "out")
	os.WriteFile(arch, makeTarGz(t, map[string][]byte{
		"encluca-0.2.0-linux-amd64/LEGGIMI.md": []byte("leggimi"),
		"encluca-0.2.0-linux-amd64/encluca":    []byte("BIN"),
	}, nil), 0o644)
	if err := extractBinary(arch, "0.2.0", "linux", "amd64", out); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(out)
	fi, _ := os.Stat(out)
	if string(got) != "BIN" || fi.Mode().Perm() != 0o755 {
		t.Fatalf("contenuto %q permessi %o", got, fi.Mode().Perm())
	}

	// voce mancante (archivio di un'altra versione)
	os.WriteFile(arch, makeTarGz(t, map[string][]byte{"encluca-0.1.9-linux-amd64/encluca": []byte("OLD")}, nil), 0o644)
	if err := extractBinary(arch, "0.2.0", "linux", "amd64", filepath.Join(dir, "o2")); err == nil {
		t.Error("archivio senza la voce accettato")
	}

	// dimensione dichiarata oltre il limite: errore senza leggerla
	os.WriteFile(arch, makeTarGz(t, map[string][]byte{"encluca-0.2.0-linux-amd64/encluca": nil},
		map[string]int64{"encluca-0.2.0-linux-amd64/encluca": maxBinarySize + 1}), 0o644)
	if err := extractBinary(arch, "0.2.0", "linux", "amd64", filepath.Join(dir, "o3")); err == nil {
		t.Error("binario troppo grande accettato")
	}
	if _, err := os.Stat(filepath.Join(dir, "o3")); err == nil {
		t.Error("file di uscita lasciato dopo un errore")
	}

	// zip per windows
	zp := filepath.Join(dir, "a.zip")
	os.WriteFile(zp, makeZip(t, map[string][]byte{
		"encluca-0.2.0-windows-amd64/LEGGIMI.md":  []byte("x"),
		"encluca-0.2.0-windows-amd64/encluca.exe": []byte("WIN"),
	}), 0o644)
	wout := filepath.Join(dir, "w.exe")
	if err := extractBinary(zp, "0.2.0", "windows", "amd64", wout); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(wout); string(got) != "WIN" {
		t.Fatalf("zip: %q", got)
	}
	os.WriteFile(zp, makeZip(t, map[string][]byte{"encluca-0.2.0-windows-amd64/altro.exe": []byte("x")}), 0o644)
	if err := extractBinary(zp, "0.2.0", "windows", "amd64", filepath.Join(dir, "w2")); err == nil {
		t.Error("zip senza la voce accettato")
	}
}
