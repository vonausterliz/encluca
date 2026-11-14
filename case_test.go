package main

import (
	"os"
	"strings"
	"testing"
)

func caseVariants(c *Client, lower string) []string {
	var out []string
	for _, f := range listWork(c) {
		if strings.EqualFold(f, lower) {
			out = append(out, f)
		}
	}
	return out
}

func hasProblem(c *Client, sub string) bool {
	probs, _ := c.Status()["problems"].([]string)
	for _, p := range probs {
		if strings.Contains(p, sub) {
			return true
		}
	}
	return false
}

// Su macOS e Windows "Foto.jpg" e "foto.jpg" sono lo stesso file: se arrivano entrambi da
// un PC Linux non devono sovrascriversi né far cancellare l'altro sul NAS.
func TestCaseCollisionKeepsBothFiles(t *testing.T) {
	env, a, b := twoOpen(t)
	b.mu.Lock()
	b.caseFold = true // B si comporta come un disco che non distingue le maiuscole
	b.mu.Unlock()
	writeWork(t, a, "Foto.jpg", "uno")
	writeWork(t, a, "foto.jpg", "due")
	waitFor(t, "due file sul NAS", func() bool { return pendingCount(a) == 0 && env.hubFiles() == 2 })
	waitFor(t, "B allineato", func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		env.hub.mu.Lock()
		defer env.hub.mu.Unlock()
		return b.st.Seq >= env.hub.idx.Seq
	})
	if v := caseVariants(b, "foto.jpg"); len(v) != 1 {
		t.Fatalf("su B devono esserci esattamente uno dei due file, trovati %v", v)
	}
	if !hasProblem(b, "maiuscole") {
		t.Fatalf("manca l'avviso su B: %v", b.Status()["problems"])
	}
	// la scansione di B non deve cancellare né modificare nulla sul NAS
	b.mu.Lock()
	b.scanLocked(false)
	b.mu.Unlock()
	waitFor(t, "niente in coda su B", func() bool { return pendingCount(b) == 0 })
	for _, rel := range []string{"Foto.jpg", "foto.jpg"} {
		if e := hubEntry(env, a, rel); e == nil || e.Del {
			t.Fatalf("%s cancellato dal NAS", rel)
		}
	}
	if readWork(a, "Foto.jpg") != "uno" || readWork(a, "foto.jpg") != "due" {
		t.Fatal("i file su A sono cambiati")
	}
	// si chiude e si riapre: stesso risultato, nessuna perdita
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	oldProbe := caseFoldProbe
	caseFoldProbe = func(string) bool { return true }
	err := b.Open(dirPw)
	caseFoldProbe = oldProbe
	if err != nil {
		t.Fatal(err)
	}
	if v := caseVariants(b, "foto.jpg"); len(v) != 1 {
		t.Fatalf("dopo la riapertura su B: %v", v)
	}
	if !hasProblem(b, "maiuscole") {
		t.Fatal("manca l'avviso dopo la riapertura")
	}
	if s, _ := b.Status()["lastError"].(string); strings.Contains(s, "non decifrabili") {
		t.Fatalf("la coincidenza di nomi non è un errore di decifratura: %s", s)
	}
	// rinominando uno dei due su A, su B compaiono entrambi
	os.Rename(workPath(a, "foto.jpg"), workPath(a, "seconda.jpg"))
	waitFor(t, "entrambi su B", func() bool {
		return readWork(b, "seconda.jpg") == "due" && readWork(b, "Foto.jpg") == "uno"
	})
	waitFor(t, "avviso sparito", func() bool { return !hasProblem(b, "maiuscole") })
}

func TestCaseFoldProbe(t *testing.T) {
	// il disco dei test su Linux distingue le maiuscole; su macOS/Windows di solito no:
	// qui si verifica solo che la prova non lasci file e non vada in errore
	d := t.TempDir()
	_ = caseFoldProbe(d)
	if ents, _ := os.ReadDir(d); len(ents) != 0 {
		t.Fatalf("la prova ha lasciato file: %v", ents)
	}
}
