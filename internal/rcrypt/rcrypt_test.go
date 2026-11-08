package rcrypt

import (
	"bytes"
	"crypto/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	c, err := New("password segreta", "sale-di-prova")
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{0, 1, 100, blockDataSize - 1, blockDataSize, blockDataSize + 1, 3*blockDataSize + 17} {
		data := make([]byte, size)
		rand.Read(data)
		enc, err := c.Seal(data)
		if err != nil {
			t.Fatal(err)
		}
		if int64(len(enc)) != EncryptedSize(int64(size)) {
			t.Fatalf("size %d: dimensione cifrata %d, attesa %d", size, len(enc), EncryptedSize(int64(size)))
		}
		dec, err := c.Open(enc)
		if err != nil || !bytes.Equal(dec, data) {
			t.Fatalf("size %d: round trip fallito: %v", size, err)
		}
	}
	for _, p := range []string{"a", "Documenti/Tasse 2026/730.pdf", "àèìòù €/日本語.txt"} {
		e, err := c.EncryptPath(p)
		if err != nil {
			t.Fatal(err)
		}
		d, err := c.DecryptPath(e)
		if err != nil || d != p {
			t.Fatalf("nome %q -> %q -> %q (%v)", p, e, d, err)
		}
	}
	other, _ := New("altra password", "sale-di-prova")
	enc, _ := c.Seal([]byte("ciao"))
	if _, err := other.Open(enc); err == nil {
		t.Fatal("decifrato con la password sbagliata")
	}
}

// TestRcloneCompat verifica la compatibilità bidirezionale con il binario rclone, se presente.
func TestRcloneCompat(t *testing.T) {
	rclone, err := exec.LookPath("rclone")
	if err != nil {
		t.Skip("rclone non installato")
	}
	pw, salt := "Pa55word di test!", "sale-casuale-0123456789"
	obscure := func(s string) string {
		out, err := exec.Command(rclone, "obscure", s).Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	dir := t.TempDir()
	raw := filepath.Join(dir, "raw")
	os.MkdirAll(raw, 0o755)
	env := append(os.Environ(),
		"RCLONE_CONFIG_CS_TYPE=crypt",
		"RCLONE_CONFIG_CS_REMOTE="+raw,
		"RCLONE_CONFIG_CS_PASSWORD="+obscure(pw),
		"RCLONE_CONFIG_CS_PASSWORD2="+obscure(salt),
		"RCLONE_CONFIG="+filepath.Join(dir, "none.conf"),
	)
	run := func(args ...string) []byte {
		cmd := exec.Command(rclone, args...)
		cmd.Env = env
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("rclone %v: %v\n%s", args, err, stderr.String())
		}
		return out
	}
	c, err := New(pw, salt)
	if err != nil {
		t.Fatal(err)
	}

	// 1) rclone cifra -> noi decifriamo
	data := make([]byte, 2*blockDataSize+333)
	rand.Read(data)
	plainFile := filepath.Join(dir, "plain.bin")
	os.WriteFile(plainFile, data, 0o644)
	run("copyto", plainFile, "cs:Cartella Uno/file è.bin")
	encPath, err := c.EncryptPath("Cartella Uno/file è.bin")
	if err != nil {
		t.Fatal(err)
	}
	encData, err := os.ReadFile(filepath.Join(raw, filepath.FromSlash(encPath)))
	if err != nil {
		t.Fatalf("file cifrato da rclone non trovato al percorso atteso %s: %v", encPath, err)
	}
	dec, err := c.Open(encData)
	if err != nil || !bytes.Equal(dec, data) {
		t.Fatalf("decifratura di un file rclone fallita: %v", err)
	}

	// 2) noi cifriamo -> rclone decifra
	ours := []byte("contenuto scritto da encluca\n")
	p2, _ := c.EncryptPath("Altra/note.txt")
	enc2, _ := c.Seal(ours)
	full := filepath.Join(raw, filepath.FromSlash(p2))
	os.MkdirAll(filepath.Dir(full), 0o755)
	os.WriteFile(full, enc2, 0o644)
	got := run("cat", "cs:Altra/note.txt")
	if !bytes.Equal(got, ours) {
		t.Fatalf("rclone ha letto %q, atteso %q", got, ours)
	}
}
