package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSuggestName(t *testing.T) {
	fits := func(s string) bool { return len(s) <= 143 } // limite reale dei byte in chiaro
	none := func(string) bool { return false }
	calvino := "Il cavaliere inesistente _ presentazione dell'autore -- Italo Calvino -- Oscar opere di Italo Calvino, 2000 -- A_ Mondadori -- isbn13 9788804370864 -- 5f6a163e739f0277160fecb4490c2234 -- Anna’s Archive.epub"
	got := suggestName(calvino, fits, none)
	if !fits(got) || !strings.HasPrefix(got, "Il cavaliere inesistente") || !strings.HasSuffix(got, ".epub") || strings.Contains(got, "isbn13") {
		t.Fatalf("suggerimento inatteso: %q", got)
	}
	// senza separatori: taglio a fine parola, estensione conservata
	plain := strings.Repeat("parola ", 40) + "fine.pdf"
	got = suggestName(plain, fits, none)
	if !fits(got) || !strings.HasSuffix(got, "parola.pdf") {
		t.Fatalf("taglio errato: %q", got)
	}
	// nome occupato (anche con maiuscole diverse): si aggiunge un numero
	taken := func(n string) bool { return strings.EqualFold(n, "Il cavaliere inesistente _ presentazione dell'autore -- Italo Calvino -- Oscar opere di Italo Calvino, 2000 -- A_ Mondadori.epub") }
	got = suggestName(calvino, fits, taken)
	if taken(got) || !fits(got) || !strings.Contains(got, "(2)") {
		t.Fatalf("collisione non evitata: %q", got)
	}
}

func TestRenameSuggestionAvoidsVaultNames(t *testing.T) {
	env := newTestEnv(t)
	pc := env.newClient("PC1")
	if err := pc.CreateVault("una password lunga e sicura"); err != nil {
		t.Fatal(err)
	}
	head := strings.Repeat("Titolo molto lungo ", 5)
	tail := " -- " + strings.Repeat("x", 80) + ".epub"
	// già nella cassaforte, con maiuscole diverse da quelle che verrebbero suggerite
	writeWork(t, pc, strings.ToUpper(strings.TrimSpace(head))+".epub", "esistente")
	waitFor(t, "upload", func() bool { return pendingCount(pc) == 0 && env.hubFiles() == 1 })
	// due nomi lunghi che porterebbero allo stesso suggerimento
	a, b := head+"-- A"+tail, head+"-- B"+tail
	writeWork(t, pc, a, "A")
	writeWork(t, pc, b, "B")
	var sa, sb string
	waitFor(t, "suggerimenti", func() bool {
		pc.mu.Lock()
		defer pc.mu.Unlock()
		sa, sb = pc.renames[a], pc.renames[b]
		return sa != "" && sb != ""
	})
	if strings.EqualFold(sa, sb) {
		t.Fatalf("stesso suggerimento per due file: %q", sa)
	}
	for _, s := range []string{sa, sb} {
		if strings.EqualFold(s, strings.ToUpper(strings.TrimSpace(head))+".epub") {
			t.Fatalf("suggerito un nome già nella cassaforte: %q", s)
		}
	}
	// la rinomina verso un nome esistente viene rifiutata, quella suggerita riesce
	if err := pc.Rename(a, strings.ToLower(strings.TrimSpace(head))+".epub"); err == nil {
		t.Fatal("rinomina verso un nome esistente accettata")
	}
	if err := pc.Rename(a, sa); err != nil {
		t.Fatal(err)
	}
	if err := pc.Rename(b, sb); err != nil {
		t.Fatal(err)
	}
	if err := pc.Rename(b, "x.epub"); err == nil {
		t.Fatal("accettata la rinomina di un elemento non più segnalato")
	}
	if err := pc.Rename("../fuori", "x"); err == nil {
		t.Fatal("accettato un percorso arbitrario")
	}
	waitFor(t, "tutto cifrato", func() bool { return pendingCount(pc) == 0 && env.hubFiles() == 3 })
	if readWork(pc, sa) != "A" || readWork(pc, sb) != "B" {
		t.Fatal("contenuti non corrispondenti dopo la rinomina")
	}
	if _, err := os.Stat(filepath.Join(pc.cfg.WorkDir, a)); !os.IsNotExist(err) {
		t.Fatal("il vecchio nome esiste ancora")
	}
	// un file presente solo nella cassaforte (es. in arrivo dal NAS, non ancora in chiaro)
	pc.mu.Lock()
	enc, _ := pc.encPathLocked("Sottocartella/Solo nel NAS.epub")
	pc.st.Files[enc] = &fstate{Rev: 7}
	takenSub := pc.nameTakenLocked("Sottocartella", "solo nel nas.epub", "")
	takenRoot := pc.nameTakenLocked(".", "solo nel nas.epub", "")
	delete(pc.st.Files, enc)
	pc.mu.Unlock()
	if !takenSub {
		t.Fatal("non riconosciuto un nome presente solo nella cassaforte")
	}
	if takenRoot {
		t.Fatal("nome di una sottocartella considerato occupato nella radice")
	}
	if err := pc.Close(); err != nil {
		t.Fatalf("chiusura: %v", err)
	}
}
