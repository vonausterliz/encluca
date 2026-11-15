package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fpOf(c *x509.Certificate) string {
	s := sha256.Sum256(c.Raw)
	return hex.EncodeToString(s[:])
}

// signWithCA firma, con la CA dell'hub, un certificato server per il nome o l'IP indicato.
func signWithCA(t *testing.T, h *Hub, host string) *x509.Certificate {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: host},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, h.caCert, &key.PublicKey, h.caKey)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return c
}

func verifyWith(ca, leaf *x509.Certificate, name string) error {
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	_, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: name, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
	return err
}

// served restituisce la catena di certificati che il server di prova presenta a una nuova connessione.
func served(t *testing.T, env *testEnv) []*x509.Certificate {
	t.Helper()
	conn, err := tls.Dial("tcp", strings.TrimPrefix(env.srv.URL, "https://"), &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	return conn.ConnectionState().PeerCertificates
}

func TestCANameConstraints(t *testing.T) {
	env := newTestEnv(t)
	ca := env.hub.caCert
	if ca == nil || !ca.IsCA || !ca.PermittedDNSDomainsCritical {
		t.Fatalf("CA non valida: %+v", ca)
	}
	if len(ca.ExtKeyUsage) != 1 || ca.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth {
		t.Fatalf("usi della CA: %v", ca.ExtKeyUsage)
	}
	found := false
	for _, n := range ca.PermittedIPRanges {
		if n.String() == "192.168.0.0/16" {
			found = true
		}
	}
	if !found {
		t.Fatalf("intervalli ammessi: %v", ca.PermittedIPRanges)
	}
	if err := verifyWith(ca, signWithCA(t, env.hub, "192.168.5.124"), "192.168.5.124"); err != nil {
		t.Fatalf("indirizzo privato rifiutato: %v", err)
	}
	if err := verifyWith(ca, signWithCA(t, env.hub, "nas.local"), "nas.local"); err != nil {
		t.Fatalf("nome locale rifiutato: %v", err)
	}
	// una CA rubata non deve poter firmare per siti o indirizzi pubblici
	for _, host := range []string{"example.com", "8.8.8.8", "banca.it"} {
		if err := verifyWith(ca, signWithCA(t, env.hub, host), host); err == nil {
			t.Fatalf("certificato per %s accettato", host)
		}
	}
}

func TestHostAllowedByCA(t *testing.T) {
	for _, h := range []string{"192.168.5.124", "10.1.2.3", "172.20.0.5", "100.100.1.1", "127.0.0.1", "nas.local", "truenas.lan", "nas.home.arpa", "a.b.internal", "fd00::1", "NAS.Local"} {
		if !hostAllowedByCA(h) {
			t.Errorf("%q deve essere ammesso", h)
		}
	}
	for _, h := range []string{"8.8.8.8", "172.32.0.1", "example.com", "local", ".local", "nas.local.evil.com", "truenas", "", "a b.local", "nas.local:8443", "2001:db8::1", "-x.local"} {
		if hostAllowedByCA(h) {
			t.Errorf("%q deve essere rifiutato", h)
		}
	}
}

func TestCAPersistsAcrossRestart(t *testing.T) {
	env := newTestEnv(t)
	h2, err := newHub(env.hub.dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if env.hub.caFingerprint == "" || h2.caFingerprint != env.hub.caFingerprint {
		t.Fatalf("la CA è cambiata al riavvio: %q %q", env.hub.caFingerprint, h2.caFingerprint)
	}
	fi, err := os.Stat(filepath.Join(env.hub.dir, "ca-key.pem"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("chiave della CA: %v %v", err, fi)
	}
}

func TestLegacyServedUntilActivated(t *testing.T) {
	env := newTestEnv(t)
	chain := served(t, env)
	if len(chain) != 1 || fpOf(chain[0]) != env.hub.fingerprint {
		t.Fatalf("prima dell'attivazione va servito il certificato autofirmato (catena di %d)", len(chain))
	}
	env.hub.mu.Lock()
	env.hub.cfg.CA.Active = true
	env.hub.mu.Unlock()
	chain = served(t, env)
	if len(chain) != 2 || fpOf(chain[1]) != env.hub.caFingerprint {
		t.Fatalf("dopo l'attivazione: catena di %d", len(chain))
	}
	if err := verifyWith(env.hub.caCert, chain[0], "encluca-hub.internal"); err != nil {
		t.Fatalf("la foglia non è valida per la CA: %v", err)
	}
}

func TestLeafHostsAndRenewal(t *testing.T) {
	env := newTestEnv(t)
	h := env.hub
	h.mu.Lock()
	h.cfg.CA.Active = true
	h.addHostLocked("192.168.5.124")
	h.addHostLocked("nas.local")
	h.mu.Unlock()
	leaf := served(t, env)[0]
	if err := verifyWith(h.caCert, leaf, "192.168.5.124"); err != nil {
		t.Fatalf("IP non nel certificato: %v", err)
	}
	if err := verifyWith(h.caCert, leaf, "nas.local"); err != nil {
		t.Fatalf("nome non nel certificato: %v", err)
	}
	if d := time.Until(leaf.NotAfter); d < 364*24*time.Hour || d > 366*24*time.Hour {
		t.Fatalf("validità: %v", d)
	}
	// una foglia che scade tra 10 giorni viene rinnovata da sola, senza toccare la CA
	caBefore := h.caFingerprint
	h.mu.Lock()
	h.leafValidity = 10 * 24 * time.Hour
	if err := h.issueLeafLocked(); err != nil {
		t.Fatal(err)
	}
	h.leafValidity = 0
	h.mu.Unlock()
	renewed := served(t, env)[0]
	if d := time.Until(renewed.NotAfter); d < 364*24*time.Hour {
		t.Fatalf("foglia non rinnovata: scade tra %v", d)
	}
	if h.caFingerprint != caBefore {
		t.Fatal("il rinnovo ha cambiato la CA")
	}
}

func TestHostsLearnedFromAuthenticatedRequests(t *testing.T) {
	env := newTestEnv(t)
	admin := adminClient(t, env)
	hosts := func() string {
		env.hub.mu.Lock()
		defer env.hub.mu.Unlock()
		return strings.Join(env.hub.cfg.CA.Hosts, ",")
	}
	srvURL, _ := url.Parse(env.srv.URL)
	call := func(c *http.Client, path, host, tok string) {
		req, _ := http.NewRequest(http.MethodGet, env.srv.URL+path, nil)
		req.Host = host
		if c == admin {
			// con un Host diverso il client non manderebbe il cookie di sessione: lo si aggiunge a mano
			for _, ck := range admin.Jar.Cookies(srvURL) {
				req.AddCookie(ck)
			}
			c = insecureClient()
		}
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
			req.Header.Set("X-Encluca-Proto", "2")
		}
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	base := hosts()                                                  // il login di adminClient ha già usato l'indirizzo del server di prova
	call(insecureClient(), "/admin/api/status", "10.0.0.9:8443", "") // non autenticata
	call(admin, "/admin/api/status", "evil.com", "")
	call(admin, "/admin/api/status", "8.8.8.8:8443", "")
	if hosts() != base {
		t.Fatalf("aggiunti indirizzi non ammessi o non autenticati: %q", hosts())
	}
	call(admin, "/admin/api/status", "192.168.5.124:8443", "")
	if !strings.Contains(hosts(), "192.168.5.124") {
		t.Fatalf("indirizzo dell'amministratore non aggiunto: %q", hosts())
	}
	call(insecureClient(), "/api/v1/changes?since=0", "nas.local:8443", env.deviceToken("PC"))
	if !strings.Contains(hosts(), "nas.local") {
		t.Fatalf("indirizzo del dispositivo non aggiunto: %q", hosts())
	}
}
