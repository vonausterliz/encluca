package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
)

var tokenRe = regexp.MustCompile(`name="encluca-token" content="([0-9a-f]{64})"`)

type uiTest struct {
	t      *testing.T
	srv    *httptest.Server
	opened []string // file passati al browser
}

func newUITest(t *testing.T, c *Client) *uiTest {
	u := &uiTest{t: t}
	oldOpen, oldDir := openInBrowser, unlockFileDir
	openInBrowser = func(p string) error { u.opened = append(u.opened, p); return nil }
	home := t.TempDir()
	unlockFileDir = func() string { return home }
	t.Cleanup(func() { openInBrowser, unlockFileDir = oldOpen, oldDir })
	u.srv = httptest.NewServer(securityHeaders(c.uiHandler("127.0.0.1:8484")))
	t.Cleanup(u.srv.Close)
	return u
}

// do fa una richiesta; cookie "" = nessuna sessione. Restituisce stato, corpo e cookie ricevuto.
func (u *uiTest) do(method, path, cookie string, hdr bool) (int, string, string) {
	u.t.Helper()
	req, _ := http.NewRequest(method, u.srv.URL+path, strings.NewReader("{}"))
	req.Host = "127.0.0.1:8484"
	if hdr {
		req.Header.Set("X-Encluca", "1")
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: "el_ui", Value: cookie})
	}
	hc := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := hc.Do(req)
	if err != nil {
		u.t.Fatal(err)
	}
	defer resp.Body.Close()
	var b bytes.Buffer
	b.ReadFrom(resp.Body)
	got := ""
	for _, ck := range resp.Cookies() {
		if ck.Name == "el_ui" {
			got = ck.Value
			if !ck.HttpOnly || ck.SameSite != http.SameSiteStrictMode {
				u.t.Fatalf("cookie di sessione non protetto: %+v", ck)
			}
		}
	}
	return resp.StatusCode, b.String(), got
}

// Su un PC con più utenti, chi apre http://127.0.0.1:8484 senza essere l'utente del servizio
// non deve ricevere il token che comanda la cassaforte.
func TestUIPageNeedsSession(t *testing.T) {
	u := newUITest(t, newBareClient(t))
	_, page, _ := u.do("GET", "/", "", false)
	if tokenRe.MatchString(page) {
		t.Fatal("la pagina senza sessione contiene il token")
	}
	if _, page, _ = u.do("GET", "/", "cookie-inventato", false); tokenRe.MatchString(page) {
		t.Fatal("token consegnato con un cookie falso")
	}
}

func TestUIUnlockFlow(t *testing.T) {
	c := newBareClient(t)
	u := newUITest(t, c)
	// la richiesta di sblocco deve venire dalla pagina (intestazione X-Encluca), non da un sito qualunque
	if code, _, _ := u.do("POST", "/unlock", "", false); code != http.StatusForbidden || len(u.opened) != 0 {
		t.Fatalf("sblocco senza intestazione: %d, aperture %d", code, len(u.opened))
	}
	if code, body, _ := u.do("POST", "/unlock", "", true); code != 200 || len(u.opened) != 1 {
		t.Fatalf("sblocco: %d %s, aperture %d", code, body, len(u.opened))
	}
	// il biglietto viaggia in un file leggibile solo dall'utente, mai sulla riga di comando
	fi, err := os.Stat(u.opened[0])
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("file di accesso: %v %v", err, fi)
	}
	b, _ := os.ReadFile(u.opened[0])
	m := regexp.MustCompile(`http://127\.0\.0\.1:8484/\?ticket=([0-9a-f]{64})`).FindStringSubmatch(string(b))
	if m == nil {
		t.Fatalf("file di accesso senza biglietto: %s", b)
	}
	// una seconda richiesta ravvicinata non apre un'altra scheda
	if u.do("POST", "/unlock", "", true); len(u.opened) != 1 {
		t.Fatalf("aperture ravvicinate: %d", len(u.opened))
	}
	if _, _, ck := u.do("GET", "/?ticket="+strings.Repeat("0", 64), "", false); ck != "" {
		t.Fatal("sessione concessa con un biglietto falso")
	}
	_, _, ck := u.do("GET", "/?ticket="+m[1], "", false)
	if ck == "" {
		t.Fatal("il biglietto valido non ha aperto la sessione")
	}
	if _, err := os.Stat(u.opened[0]); !os.IsNotExist(err) {
		t.Fatal("il file di accesso non è stato cancellato dopo l'uso")
	}
	if _, _, again := u.do("GET", "/?ticket="+m[1], "", false); again != "" {
		t.Fatal("biglietto usato due volte")
	}
	_, page, _ := u.do("GET", "/", ck, false)
	tm := tokenRe.FindStringSubmatch(page)
	if tm == nil {
		t.Fatal("con la sessione la pagina deve contenere il token")
	}
	// la sessione sopravvive al riavvio del servizio
	u2 := newUITest(t, c)
	if _, page, _ := u2.do("GET", "/", ck, false); !tokenRe.MatchString(page) {
		t.Fatal("sessione persa dopo il riavvio")
	}
}

func TestBrowserDesktopID(t *testing.T) {
	for in, want := range map[string]string{
		"brave-browser.desktop\n": "brave-browser.desktop",
		"firefox_firefox.desktop": "firefox_firefox.desktop",
		"":                        "",
		"xdg-settings: unknown\n": "",
		"-x.desktop":              "",
		"a/b.desktop":             "",
		"a.desktop\nb.desktop\n":  "",
	} {
		if got := browserDesktopID(in); got != want {
			t.Errorf("browserDesktopID(%q) = %q, want %q", in, got, want)
		}
	}
}
