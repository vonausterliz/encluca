package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// openClient: un PC abbinato con la cassaforte appena creata e aperta.
func openClient(t *testing.T) (*testEnv, *Client) {
	t.Helper()
	env := newTestEnv(t)
	c := env.newClient("PC1")
	if err := c.CreateVault(dirPw); err != nil {
		t.Fatal(err)
	}
	return env, c
}

func acStatus(t *testing.T, c *Client) autoCloseStatus {
	t.Helper()
	s, ok := c.Status()["autoClose"].(autoCloseStatus)
	if !ok {
		t.Fatalf("Status senza autoClose: %#v", c.Status()["autoClose"])
	}
	return s
}

func isOpen(c *Client) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cip != nil
}

func near(a, b time.Time) bool { d := a.Sub(b); return d > -5*time.Second && d < 5*time.Second }

func TestAutoCloseDefault(t *testing.T) {
	c := newBareClient(t)
	if m := acStatus(t, c).Minutes; m != 30 {
		t.Fatalf("minuti predefiniti = %d, voluti 30", m)
	}
}

func TestAutoCloseAfterIdle(t *testing.T) {
	env, c := openClient(t)
	writeWork(t, c, "a.txt", "ciao")
	waitFor(t, "invio di a.txt", func() bool { return env.hubFiles() == 1 && pendingCount(c) == 0 })
	c.autoCloseTick(time.Now().Add(31 * time.Minute))
	if isOpen(c) {
		t.Fatal("cassaforte ancora aperta dopo 31 minuti senza modifiche")
	}
	if _, err := os.Stat(c.cfg.WorkDir); !os.IsNotExist(err) {
		t.Fatalf("cartella in chiaro ancora presente: %v", err)
	}
	if !strings.Contains(strings.Join(c.log.Lines(), "\n"), "chiusa automaticamente") {
		t.Fatal("manca la riga nel registro")
	}
}

func TestAutoCloseNotBefore(t *testing.T) {
	_, c := openClient(t)
	c.autoCloseTick(time.Now().Add(27 * time.Minute))
	if !isOpen(c) {
		t.Fatal("chiusa prima del tempo")
	}
	if acStatus(t, c).Warn {
		t.Fatal("avviso troppo presto")
	}
	c.mu.Lock()
	c.lastActivity = time.Now().Add(-29 * time.Minute)
	c.mu.Unlock()
	if !acStatus(t, c).Warn {
		t.Fatal("manca l'avviso a 1 minuto dalla chiusura")
	}
	c.autoCloseTick(time.Now())
	if !isOpen(c) {
		t.Fatal("chiusa a 29 minuti")
	}
}

func TestAutoCloseDisabled(t *testing.T) {
	_, c := openClient(t)
	if err := c.SetAutoClose(0); err != nil {
		t.Fatal(err)
	}
	c.autoCloseTick(time.Now().Add(10 * time.Hour))
	if !isOpen(c) {
		t.Fatal("chiusa con chiusura automatica disattivata")
	}
	if !acStatus(t, c).CloseAt.IsZero() {
		t.Fatal("closeAt impostato con chiusura disattivata")
	}
}

func TestAutoCloseActivityResets(t *testing.T) {
	_, c := openClient(t)
	before := acStatus(t, c).CloseAt
	time.Sleep(1100 * time.Millisecond)
	writeWork(t, c, "b.txt", "modifica")
	waitFor(t, "spostamento della chiusura", func() bool { return acStatus(t, c).CloseAt.After(before) })
}

func TestAutoCloseBlockedRetries(t *testing.T) {
	_, c := openClient(t)
	long := strings.Repeat("Il cavaliere inesistente ", 8) + "– Anna’s Archive.epub"
	writeWork(t, c, long, "contenuto unico")
	waitFor(t, "segnalazione del nome lungo", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.problems[long] != ""
	})
	t0 := time.Now()
	c.mu.Lock()
	c.lastActivity = t0
	c.mu.Unlock()
	c.autoCloseTick(t0.Add(31 * time.Minute))
	if !isOpen(c) || readWork(c, long) != "contenuto unico" {
		t.Fatal("chiusa con un file non cifrabile")
	}
	if acStatus(t, c).Error == "" {
		t.Fatal("motivo del blocco non mostrato")
	}
	c.mu.Lock()
	next := c.autoCloseNext
	c.mu.Unlock()
	if !next.Equal(t0.Add(36 * time.Minute)) {
		t.Fatalf("nuovo tentativo previsto a %v, voluto dopo 5 minuti", next.Sub(t0))
	}
	if err := os.Rename(filepath.Join(c.cfg.WorkDir, long), filepath.Join(c.cfg.WorkDir, "corto.epub")); err != nil {
		t.Fatal(err)
	}
	// la rinomina è un'attività: azzera il blocco e fa ripartire il conteggio
	waitFor(t, "rinomina vista", func() bool { return acStatus(t, c).Error == "" })
	c.autoCloseTick(acStatus(t, c).CloseAt.Add(time.Second))
	if isOpen(c) {
		t.Fatal("non chiusa dopo aver risolto il problema")
	}
}

func TestAutoCloseSkipsWhenBusy(t *testing.T) {
	_, c := openClient(t)
	c.mu.Lock()
	c.busy = "apertura"
	c.mu.Unlock()
	c.autoCloseTick(time.Now().Add(31 * time.Minute))
	c.mu.Lock()
	busy, open := c.busy, c.cip != nil
	c.busy = ""
	c.mu.Unlock()
	if !open || busy != "apertura" {
		t.Fatalf("chiusura durante un'altra operazione (open=%v busy=%q)", open, busy)
	}
}

func TestAutoClosePostpone(t *testing.T) {
	_, c := openClient(t)
	c.mu.Lock()
	c.lastActivity = time.Now().Add(-29 * time.Minute)
	c.mu.Unlock()
	if !acStatus(t, c).Warn {
		t.Fatal("manca l'avviso")
	}
	c.PostponeAutoClose()
	s := acStatus(t, c)
	if s.Warn || s.Error != "" || !near(s.CloseAt, time.Now().Add(30*time.Minute)) {
		t.Fatalf("rimando non applicato: %+v", s)
	}
}

func TestAutoCloseReopenResets(t *testing.T) {
	_, c := openClient(t)
	c.autoCloseTick(time.Now().Add(31 * time.Minute))
	if isOpen(c) {
		t.Fatal("non chiusa")
	}
	if err := c.Open(dirPw); err != nil {
		t.Fatal(err)
	}
	if s := acStatus(t, c); !near(s.CloseAt, time.Now().Add(30*time.Minute)) {
		t.Fatalf("conteggio non ripartito: %+v", s)
	}
}

func TestSetAutoCloseValidates(t *testing.T) {
	c := newBareClient(t)
	for _, bad := range []int{-1, 1441} {
		if err := c.SetAutoClose(bad); err == nil {
			t.Fatalf("accettato %d", bad)
		}
	}
	if err := c.SetAutoClose(60); err != nil {
		t.Fatal(err)
	}
	c2, err := newClient(c.dir)
	if err != nil {
		t.Fatal(err)
	}
	if m := acStatus(t, c2).Minutes; m != 60 {
		t.Fatalf("dopo il riavvio minuti = %d", m)
	}
}

func TestAutoCloseAPI(t *testing.T) {
	c := newBareClient(t)
	if code, out := apiCall(t, c, "/api/autoclose", map[string]any{"Minutes": 45}); code != 200 {
		t.Fatalf("impostazione: %d %v", code, out)
	}
	_, st := apiCall(t, c, "/api/status", nil)
	if m := st["autoClose"].(map[string]any)["minutes"]; m != 45.0 {
		t.Fatalf("minuti nello stato: %v", m)
	}
	if code, _ := apiCall(t, c, "/api/autoclose", map[string]any{"Minutes": 5000}); code != 400 {
		t.Fatalf("5000 minuti accettati: %d", code)
	}
	if code, out := apiCall(t, c, "/api/autoclose/postpone", map[string]any{}); code != 200 {
		t.Fatalf("rimando: %d %v", code, out)
	}
}

// Dopo un blocco, una modifica fa ripartire il conteggio e l'avviso dei 2 minuti deve tornare.
func TestAutoCloseWarnAfterBlockedThenActivity(t *testing.T) {
	_, c := openClient(t)
	c.mu.Lock()
	c.autoCloseErr = "bloccata"
	c.autoCloseNext = time.Now().Add(time.Minute)
	c.mu.Unlock()
	writeWork(t, c, "nuovo.txt", "x")
	waitFor(t, "blocco azzerato dalla modifica", func() bool { return acStatus(t, c).Error == "" })
	c.mu.Lock()
	c.lastActivity = time.Now().Add(-29 * time.Minute)
	c.mu.Unlock()
	if !acStatus(t, c).Warn {
		t.Fatal("avviso dei 2 minuti soppresso da un blocco vecchio")
	}
}

// Cambiare l'impostazione non deve chiudere subito una cassaforte ferma da tempo.
func TestSetAutoCloseRestartsCountdown(t *testing.T) {
	_, c := openClient(t)
	c.SetAutoClose(0)
	c.mu.Lock()
	c.lastActivity = time.Now().Add(-2 * time.Hour)
	c.mu.Unlock()
	if err := c.SetAutoClose(30); err != nil {
		t.Fatal(err)
	}
	c.autoCloseTick(time.Now().Add(time.Minute))
	if !isOpen(c) {
		t.Fatal("chiusa subito dopo aver cambiato l'impostazione, senza avviso")
	}
	if s := acStatus(t, c); !near(s.CloseAt, time.Now().Add(30*time.Minute)) {
		t.Fatalf("conteggio non ripartito: %+v", s)
	}
}

// Un file salvato mentre la chiusura invia al NAS non deve essere cancellato senza essere cifrato.
func TestCloseAbortsOnLateChange(t *testing.T) {
	_, c := openClient(t)
	writeWork(t, c, "a.txt", "prima")
	waitFor(t, "a.txt cifrato", func() bool { return pendingCount(c) == 0 })
	closeBeforeRemove = func() { writeWork(t, c, "tardi.txt", "salvato all'ultimo") }
	defer func() { closeBeforeRemove = nil }()
	if err := c.Close(); err == nil {
		t.Fatal("chiusura riuscita nonostante una modifica dell'ultimo momento")
	}
	if !isOpen(c) || readWork(c, "tardi.txt") != "salvato all'ultimo" {
		t.Fatal("modifica dell'ultimo momento persa")
	}
	closeBeforeRemove = nil
	waitFor(t, "tardi.txt cifrato", func() bool { return pendingCount(c) == 0 })
	if err := c.Close(); err != nil {
		t.Fatalf("seconda chiusura: %v", err)
	}
}
