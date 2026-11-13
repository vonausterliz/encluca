package main

// Aggiornamento automatico del client: verifica di versioni, firma e pacchetti.
//
// La fiducia sta tutta nella firma ed25519 di SHA256SUMS con la chiave
// pubblica incorporata (update_key.go): l'indirizzo da cui si scarica non
// conta, quindi può essere sostituito nei test.

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

const (
	maxArchiveSize = 64 << 20
	maxBinarySize  = 128 << 20
	maxSumsSize    = 16 << 10
	maxSigSize     = 1 << 10
	maxAPISize     = 1 << 20
)

// parseVersion accetta solo "X.Y.Z" o "vX.Y.Z" con numeri decimali senza segno.
func parseVersion(s string) (v [3]int, ok bool) {
	s = strings.TrimPrefix(s, "v")
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		if p == "" || len(p) > 6 {
			return v, false
		}
		for _, r := range p {
			if r < '0' || r > '9' {
				return v, false
			}
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

// newerVersion è vera solo se a e b sono valide e a è strettamente più nuova di b.
func newerVersion(a, b string) bool {
	va, ok1 := parseVersion(a)
	vb, ok2 := parseVersion(b)
	if !ok1 || !ok2 {
		return false
	}
	for i := range va {
		if va[i] != vb[i] {
			return va[i] > vb[i]
		}
	}
	return false
}

func assetName(version, goos, goarch string) string {
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	return fmt.Sprintf("encluca-%s-%s-%s%s", strings.TrimPrefix(version, "v"), goos, goarch, ext)
}

// verifySums controlla la firma di SHA256SUMS e restituisce nome → sha256 hex.
func verifySums(sums, sig []byte, pub ed25519.PublicKey) (map[string]string, error) {
	s, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil || len(s) != ed25519.SignatureSize {
		return nil, errors.New(T("firma dell'aggiornamento illeggibile"))
	}
	if !ed25519.Verify(pub, sums, s) {
		return nil, errors.New(T("firma dell'aggiornamento non valida"))
	}
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		hash, name, ok := strings.Cut(line, "  ")
		b, herr := hex.DecodeString(hash)
		if !ok || herr != nil || len(b) != 32 || name == "" || strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
			return nil, fmt.Errorf(T("riga non valida nell'elenco delle impronte: %q"), line)
		}
		out[name] = strings.ToLower(hash)
	}
	if len(out) == 0 {
		return nil, errors.New(T("elenco delle impronte vuoto"))
	}
	return out, nil
}

// extractBinary estrae solo encluca-<ver>-<os>-<arch>/encluca[.exe] in outPath (0755).
func extractBinary(archivePath, version, goos, goarch, outPath string) (err error) {
	version = strings.TrimPrefix(version, "v")
	want := fmt.Sprintf("encluca-%s-%s-%s/encluca", version, goos, goarch)
	if goos == "windows" {
		want += ".exe"
	}
	var src io.Reader
	var size int64 = -1
	if goos == "windows" {
		zr, err := zip.OpenReader(archivePath)
		if err != nil {
			return err
		}
		defer zr.Close()
		for _, f := range zr.File {
			if f.Name != want {
				continue
			}
			if f.UncompressedSize64 > maxBinarySize {
				return errors.New(T("il programma nel pacchetto è troppo grande"))
			}
			rc, err := f.Open()
			if err != nil {
				return err
			}
			defer rc.Close()
			src, size = rc, int64(f.UncompressedSize64)
			break
		}
	} else {
		f, err := os.Open(archivePath)
		if err != nil {
			return err
		}
		defer f.Close()
		gz, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		tr := tar.NewReader(gz)
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
			if h.Name != want || h.Typeflag != tar.TypeReg {
				continue
			}
			if h.Size > maxBinarySize {
				return errors.New(T("il programma nel pacchetto è troppo grande"))
			}
			src, size = tr, h.Size
			break
		}
	}
	if src == nil {
		return fmt.Errorf(T("il pacchetto non contiene %s"), want)
	}
	out, err := os.OpenFile(outPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(outPath)
		}
	}()
	n, err := io.Copy(out, io.LimitReader(src, maxBinarySize+1))
	if err != nil {
		return err
	}
	if n > maxBinarySize || size >= 0 && n != size {
		return errors.New(T("il programma nel pacchetto è troppo grande o incompleto"))
	}
	return os.Chmod(outPath, 0o755)
}
