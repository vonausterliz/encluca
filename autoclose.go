package main

import (
	"errors"
	"time"
)

// Chiusura automatica: la cassaforte aperta si chiude da sola dopo N minuti senza modifiche
// locali ai file della cartella aperta (i file in arrivo dal NAS non contano). Usa la stessa
// Close() del pulsante: se la chiusura è bloccata non forza nulla e riprova più tardi.
const (
	autoCloseDefault = 30
	autoCloseMax     = 1440
	autoCloseWarn    = 2 * time.Minute
	autoCloseRetry   = 5 * time.Minute
	// con file aperti si ritenta presto: la chiusura riesce appena l'utente li chiude
	autoCloseRetryInUse = time.Minute
)

// errCloseBusy: un'altra apertura o chiusura è già in corso. Il testo si traduce al momento
// dell'uso (la lingua può cambiare dopo l'avvio).
type closeBusyError struct{}

func (closeBusyError) Error() string { return T("operazione in corso: riprova tra qualche secondo") }

var errCloseBusy error = closeBusyError{}

// closeBeforeRemove, se impostata (solo nei test), gira in Close() subito prima della
// cancellazione della cartella in chiaro: simula un salvataggio dell'ultimo momento.
var closeBeforeRemove func()

type autoCloseStatus struct {
	Minutes int       `json:"minutes"`
	CloseAt time.Time `json:"closeAt"`
	Warn    bool      `json:"warn"`
	Error   string    `json:"error"`
}

func (c *Client) autoCloseMinLocked() int {
	if c.cfg.AutoCloseMin == nil {
		return autoCloseDefault
	}
	return *c.cfg.AutoCloseMin
}

func (c *Client) autoCloseStatusLocked(now time.Time) autoCloseStatus {
	s := autoCloseStatus{Minutes: c.autoCloseMinLocked(), Error: c.autoCloseErr}
	if c.cip == nil || s.Minutes == 0 {
		return s
	}
	s.CloseAt = c.lastActivity.Add(time.Duration(s.Minutes) * time.Minute)
	s.Warn = s.Error == "" && !now.Before(s.CloseAt.Add(-autoCloseWarn))
	return s
}

// autoCloseRetryFor è l'attesa prima di un nuovo tentativo dopo una chiusura bloccata.
func (c *Client) autoCloseRetryFor(err error) time.Duration {
	var iu *inUseError
	if errors.As(err, &iu) {
		return autoCloseRetryInUse
	}
	return autoCloseRetry
}

// autoCloseTick chiude la cassaforte se è scaduto il tempo senza modifiche. Prende c.mu da sola.
func (c *Client) autoCloseTick(now time.Time) {
	c.mu.Lock()
	min := c.autoCloseMinLocked()
	due := c.cip != nil && c.st.Open && c.busy == "" && min > 0 &&
		!now.Before(c.lastActivity.Add(time.Duration(min)*time.Minute)) && !now.Before(c.autoCloseNext)
	c.mu.Unlock()
	if !due {
		return
	}
	err := c.Close()
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		if errors.Is(err, errCloseBusy) {
			return
		}
		if err.Error() != c.autoCloseErr {
			c.log.Printf(T("Chiusura automatica non riuscita: %v"), err)
		}
		c.autoCloseErr = err.Error()
		c.autoCloseNext = now.Add(c.autoCloseRetryFor(err))
		return
	}
	c.autoCloseErr = ""
	c.autoCloseNext = time.Time{}
	c.log.Printf(T("Cassaforte chiusa automaticamente dopo %d minuti senza modifiche"), min)
}

// PostponeAutoClose fa ripartire il conteggio (pulsante "Rimanda").
func (c *Client) PostponeAutoClose() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.noteActivityLocked(time.Now())
	c.log.Print(T("Chiusura automatica rimandata"))
}

// noteActivityLocked registra un'attività e azzera un eventuale blocco precedente.
func (c *Client) noteActivityLocked(now time.Time) {
	c.lastActivity = now
	c.autoCloseErr = ""
	c.autoCloseNext = time.Time{}
}

func (c *Client) SetAutoClose(min int) error {
	if min < 0 || min > autoCloseMax {
		return errors.New(T("indica un numero di minuti tra 0 e 1440"))
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cfg.AutoCloseMin = &min
	if c.cip != nil {
		c.noteActivityLocked(time.Now()) // il nuovo tempo parte da adesso: niente chiusura immediata
	}
	return saveJSON(c.cfgPath(), &c.cfg)
}
