package main

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// minPasswordLen è la lunghezza minima delle nuove password (cassaforte e amministratore).
// Le password già in uso continuano a funzionare.
const minPasswordLen = 14

// ipLimiter rallenta i tentativi a forza bruta, separatamente per ogni indirizzo IP:
// dopo 5 errori di fila l'IP deve aspettare 1 s, poi 2 s, 4 s… fino a 15 minuti.
// Gli altri indirizzi non sono toccati, quindi un attaccante non blocca l'amministratore.
type ipLimiter struct {
	mu sync.Mutex
	m  map[string]*ipFails
}

type ipFails struct {
	n     int
	until time.Time
	last  time.Time
}

const (
	limiterFree = 5
	limiterMax  = 15 * time.Minute
)

func newIPLimiter() *ipLimiter { return &ipLimiter{m: map[string]*ipFails{}} }

// blocked restituisce quanto deve ancora aspettare l'IP (0 = può provare).
func (l *ipLimiter) blocked(ip string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if f := l.m[ip]; f != nil {
		if d := time.Until(f.until); d > 0 {
			return d
		}
	}
	return 0
}

func (l *ipLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if len(l.m) > 10000 { // pulizia: dimentica gli IP inattivi da più di un'ora
		for k, f := range l.m {
			if now.Sub(f.last) > time.Hour {
				delete(l.m, k)
			}
		}
	}
	f := l.m[ip]
	if f == nil || now.Sub(f.last) > time.Hour {
		f = &ipFails{}
		l.m[ip] = f
	}
	f.n++
	f.last = now
	if f.n >= limiterFree {
		d := limiterMax
		if shift := f.n - limiterFree; shift < 10 {
			d = min(time.Second<<shift, limiterMax)
		}
		f.until = now.Add(d)
	}
}

func (l *ipLimiter) ok(ip string) {
	l.mu.Lock()
	delete(l.m, ip)
	l.mu.Unlock()
}

func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

func tooMany(w http.ResponseWriter, wait time.Duration) {
	secs := int(wait.Seconds()) + 1
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeErr(w, http.StatusTooManyRequests, T("troppi tentativi errati: riprova tra qualche minuto"))
}

// checkAdmin verifica la password amministratore. Lo scrypt (lento e da 32 MB) si calcola
// fuori dal lock dell'hub, così un login non blocca la sincronizzazione, e al massimo due
// alla volta, così una raffica di richieste non esaurisce memoria e CPU del NAS.
func (h *Hub) checkAdmin(pw string) bool {
	h.mu.Lock()
	salt, want := h.cfg.AdminSalt, h.cfg.AdminHash
	h.mu.Unlock()
	h.scryptSem <- struct{}{}
	got := hashAdmin(pw, salt)
	<-h.scryptSem
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}
