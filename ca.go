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
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Certificato del NAS firmato da una CA locale.
//
// Il certificato autofirmato dell'hub fa comparire nel browser l'avviso "connessione non
// privata". Per toglierlo l'hub crea una propria autorità di certificazione (CA): importata una
// volta nel browser, rende fidato il certificato del server che l'hub firma con lei.
//
// La CA è limitata (name constraints) agli indirizzi privati e ai nomi di rete locale: se la sua
// chiave venisse rubata non potrebbe firmare certificati validi per siti pubblici.
//
// Il certificato autofirmato resta: è quello servito finché l'amministratore non attiva il
// nuovo (cfg.CA.Active), ed è il ripiego se si torna indietro. I client accettano entrambi:
// l'impronta di quello vecchio, oppure una catena firmata dalla CA che hanno ricevuto.

type CAConfig struct {
	Active bool     `json:"active"`          // true: si serve il certificato firmato dalla CA
	Hosts  []string `json:"hosts,omitempty"` // indirizzi IP e nomi del NAS da mettere nel certificato
}

const (
	leafLifetime   = 365 * 24 * time.Hour
	leafRenewAhead = 30 * 24 * time.Hour
	caLifetime     = 20 * 365 * 24 * time.Hour
	maxCAHosts     = 20
	leafFixedName  = "encluca-hub.internal" // sempre presente, così il certificato ha almeno un nome
)

// Nomi che la CA può firmare: reti private, VPN (100.64/10), locali; e nomi di rete locale.
var (
	caIPRanges   = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "::1/128", "fc00::/7", "fe80::/10"}
	caDNSSuffixs = []string{"local", "lan", "home", "internal", "home.arpa", "localdomain", "fritz.box"}
)

func caNets() []*net.IPNet {
	out := make([]*net.IPNet, 0, len(caIPRanges))
	for _, s := range caIPRanges {
		_, n, _ := net.ParseCIDR(s)
		out = append(out, n)
	}
	return out
}

// hostAllowedByCA dice se host (senza porta) è un indirizzo o un nome che la CA può firmare.
func hostAllowedByCA(host string) bool {
	host = strings.ToLower(host)
	if ip := net.ParseIP(host); ip != nil {
		for _, n := range caNets() {
			if n.Contains(ip) {
				return true
			}
		}
		return false
	}
	if host == "" || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, ch := range label {
			if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-') {
				return false
			}
		}
	}
	for _, suf := range caDNSSuffixs {
		if strings.HasSuffix(host, "."+suf) {
			return true
		}
	}
	return false
}

func certFingerprint(der []byte) string {
	s := sha256.Sum256(der)
	return hex.EncodeToString(s[:])
}

// loadOrCreateCA carica la CA dell'hub o la crea la prima volta. Non viene mai rigenerata da
// sola: cambiarla obbligherebbe a reimportarla in ogni browser.
func (h *Hub) loadOrCreateCA() error {
	certFile, keyFile := filepath.Join(h.dir, "ca-cert.pem"), filepath.Join(h.dir, "ca-key.pem")
	cp, err1 := os.ReadFile(certFile)
	kp, err2 := os.ReadFile(keyFile)
	if err1 == nil && err2 == nil {
		cb, _ := pem.Decode(cp)
		kb, _ := pem.Decode(kp)
		if cb == nil || kb == nil {
			return errors.New(T("file della CA illeggibili"))
		}
		cert, err := x509.ParseCertificate(cb.Bytes)
		if err != nil {
			return err
		}
		key, err := x509.ParseECPrivateKey(kb.Bytes)
		if err != nil {
			return err
		}
		h.caCert, h.caKey, h.caFingerprint = cert, key, certFingerprint(cert.Raw)
		return nil
	}
	return h.createCA()
}

// createCA genera una nuova CA, ne scrive certificato e chiave (sostituendo quelli esistenti) e
// la rende quella in uso.
func (h *Hub) createCA() error {
	certFile, keyFile := filepath.Join(h.dir, "ca-cert.pem"), filepath.Join(h.dir, "ca-key.pem")
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	tmpl := &x509.Certificate{
		SerialNumber:                serial,
		Subject:                     pkix.Name{CommonName: "encluca-ca", Organization: []string{"encluca"}},
		NotBefore:                   time.Now().Add(-time.Hour),
		NotAfter:                    time.Now().Add(caLifetime),
		IsCA:                        true,
		BasicConstraintsValid:       true,
		MaxPathLenZero:              true,
		KeyUsage:                    x509.KeyUsageCertSign,
		ExtKeyUsage:                 []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		PermittedDNSDomainsCritical: true,
		PermittedDNSDomains:         caDNSSuffixs,
		PermittedIPRanges:           caNets(),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600); err != nil {
		return err
	}
	if err := writeFileAtomic(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		return err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return err
	}
	h.caCert, h.caKey, h.caFingerprint = cert, key, certFingerprint(der)
	return nil
}

// addHostLocked aggiunge host (anche con la porta) agli indirizzi del certificato, se è un
// indirizzo ammesso e non c'è già. Il certificato del server verrà rigenerato.
func (h *Hub) addHostLocked(host string) bool {
	if hp, _, err := net.SplitHostPort(host); err == nil {
		host = hp
	}
	host = strings.ToLower(strings.Trim(host, "[]"))
	if !hostAllowedByCA(host) || len(h.cfg.CA.Hosts) >= maxCAHosts {
		return false
	}
	for _, x := range h.cfg.CA.Hosts {
		if x == host {
			return false
		}
	}
	h.cfg.CA.Hosts = append(h.cfg.CA.Hosts, host)
	h.leaf = nil
	h.saveCfg()
	return true
}

// issueLeafLocked crea un nuovo certificato del server firmato dalla CA, valido un anno, con
// gli indirizzi configurati. La chiave resta solo in memoria: si rigenera a ogni avvio, tanto
// i client e i browser si fidano della CA, non del singolo certificato.
func (h *Hub) issueLeafLocked() error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	life := h.leafValidity
	if life == 0 {
		life = leafLifetime
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "encluca-hub"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(life),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{leafFixedName},
	}
	for _, host := range h.cfg.CA.Hosts {
		if ip := net.ParseIP(host); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, host)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, h.caCert, &key.PublicKey, h.caKey)
	if err != nil {
		return err
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		return err
	}
	// la catena include la CA: i client la riconoscono dall'impronta che hanno salvato
	h.leaf = &tls.Certificate{Certificate: [][]byte{der, h.caCert.Raw}, PrivateKey: key, Leaf: parsed}
	return nil
}

// currentCertLocked restituisce il certificato da servire adesso: quello autofirmato finché il
// nuovo non è attivo; altrimenti quello firmato dalla CA, rinnovato se manca o sta per scadere.
func (h *Hub) currentCertLocked() (*tls.Certificate, error) {
	if !h.cfg.CA.Active || h.caCert == nil {
		return &h.cert, nil
	}
	if h.leaf == nil || time.Until(h.leaf.Leaf.NotAfter) < leafRenewAhead {
		if err := h.issueLeafLocked(); err != nil {
			h.log.Printf(T("ERRORE creazione del certificato del server: %v"), err)
			return &h.cert, nil // meglio il certificato autofirmato che nessun certificato
		}
	}
	return h.leaf, nil
}

func (h *Hub) tlsConfig() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.currentCertLocked()
		},
	}
}

// servedFingerprintLocked è l'impronta del certificato che i browser vedono in questo momento.
func (h *Hub) servedFingerprintLocked() string {
	c, _ := h.currentCertLocked()
	return certFingerprint(c.Certificate[0])
}

func (h *Hub) caStatusLocked() map[string]any {
	res := map[string]any{"fingerprint": h.caFingerprint, "active": h.cfg.CA.Active, "hosts": append([]string{}, h.cfg.CA.Hosts...)}
	if h.cfg.CA.Active && h.leaf != nil {
		res["leafExpires"] = h.leaf.Leaf.NotAfter
	}
	return res
}

// ---- API ----

// adminCAActivate: POST {Active, Force}. Attivare il nuovo certificato taglierebbe fuori i
// dispositivi che non hanno ancora ricevuto la CA: senza Force l'hub rifiuta e li elenca.
func (h *Hub) adminCAActivate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, T("metodo non ammesso"))
		return
	}
	var body struct{ Active, Force bool }
	readBody(r, &body)
	h.mu.Lock()
	defer h.mu.Unlock()
	if body.Active && !body.Force {
		notReady := []string{}
		for _, d := range h.cfg.Devices {
			if d.TokenHash != "" && d.CAFP != h.caFingerprint {
				notReady = append(notReady, d.Name)
			}
		}
		if len(notReady) > 0 {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":    T("alcuni dispositivi non hanno ancora ricevuto il nuovo certificato: con l'attivazione smetterebbero di sincronizzare"),
				"notReady": notReady,
			})
			return
		}
	}
	h.cfg.CA.Active = body.Active
	if err := h.saveCfg(); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if body.Active {
		h.log.Print(T("Certificato del NAS: ora si usa quello firmato dalla CA"))
	} else {
		h.log.Print(T("Certificato del NAS: si torna a quello autofirmato"))
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// adminCARotate: POST. Crea una nuova CA (da usare se quella vecchia potrebbe essere stata
// copiata) e torna al certificato autofirmato: i dispositivi ricevono la nuova impronta sulla
// connessione verificata dal certificato autofirmato abbinato, poi l'amministratore riattiva come
// la prima volta. Da quel momento i client non accettano più la vecchia CA.
func (h *Hub) adminCARotate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, T("metodo non ammesso"))
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.createCA(); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.leaf = nil
	h.cfg.CA.Active = false
	if err := h.saveCfg(); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.log.Print(T("Certificato del NAS: creata una nuova CA. Si usa il certificato autofirmato finché non la attivi; i computer la ricevono da soli, i browser devono importarla di nuovo"))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "fingerprint": h.caFingerprint})
}

// adminCAHosts: POST {Hosts: [...]} sostituisce gli indirizzi messi nel certificato del server.
func (h *Hub) adminCAHosts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, T("metodo non ammesso"))
		return
	}
	var body struct{ Hosts []string }
	readBody(r, &body)
	var hosts []string
	seen := map[string]bool{}
	for _, x := range body.Hosts {
		x = strings.ToLower(strings.TrimSpace(x))
		if x == "" || seen[x] {
			continue
		}
		if !hostAllowedByCA(x) {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf(T("indirizzo non ammesso: %s (servono indirizzi IP privati o nomi di rete locale)"), x))
			return
		}
		seen[x] = true
		hosts = append(hosts, x)
	}
	if len(hosts) > maxCAHosts {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf(T("troppi indirizzi: al massimo %d"), maxCAHosts))
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.CA.Hosts = hosts
	h.leaf = nil
	if err := h.saveCfg(); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// serveCACert: il certificato della CA, da importare nel browser. È un dato pubblico (lo riceve
// chiunque si colleghi), quindi non richiede l'accesso.
func (h *Hub) serveCACert(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/x-x509-ca-cert")
	w.Header().Set("Content-Disposition", `attachment; filename="encluca-ca.crt"`)
	w.Header().Set("Cache-Control", "no-store")
	h.mu.Lock()
	raw := h.caCert.Raw // può cambiare con la rotazione della CA
	h.mu.Unlock()
	w.Write(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw}))
}
