package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

// Ogni T("...") del codice deve avere una traduzione inglese con gli stessi segnaposto.
func TestTranslationsComplete(t *testing.T) {
	call := regexp.MustCompile(`\bT\(("(?:[^"\\]|\\.)*")\)`)
	verbs := regexp.MustCompile(`%[-+# 0]*\d*(?:\.\d+)?[a-zA-Z%]`)
	files, _ := filepath.Glob("*.go")
	n := 0
	for _, f := range files {
		if filepath.Ext(f) != ".go" || f == "i18n.go" || len(f) > 8 && f[len(f)-8:] == "_test.go" {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range call.FindAllStringSubmatch(string(src), -1) {
			it, err := strconv.Unquote(m[1])
			if err != nil {
				t.Fatalf("%s: %v", f, err)
			}
			n++
			en, ok := enMessages[it]
			if !ok {
				t.Errorf("%s: manca la traduzione di %q", f, it)
				continue
			}
			a, b := verbs.FindAllString(it, -1), verbs.FindAllString(en, -1)
			if len(a) != len(b) {
				t.Errorf("segnaposto diversi: %q -> %q", it, en)
				continue
			}
			for i := range a {
				if a[i] != b[i] {
					t.Errorf("segnaposto diversi: %q -> %q", it, en)
					break
				}
			}
		}
	}
	if n < 80 {
		t.Fatalf("trovate solo %d chiamate a T: il controllo non sta leggendo il codice", n)
	}
}

func TestT(t *testing.T) {
	defer setLang("it")
	setLang("en")
	if got := T("password errata"); got != "wrong password" {
		t.Fatal(got)
	}
	setLang("it")
	if got := T("password errata"); got != "password errata" {
		t.Fatal(got)
	}
}
