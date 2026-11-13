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
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
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

// ---- controllo e preparazione ----

const defaultUpdateAPI = "https://api.github.com/repos/vonausterliz/encluca/releases/latest"

type updateState struct {
	Enabled   bool      `json:"enabled"`
	Current   string    `json:"current"`
	Available string    `json:"available,omitempty"` // versione più nuova trovata
	Staged    string    `json:"staged,omitempty"`    // scaricata e verificata, pronta
	LastCheck time.Time `json:"lastCheck"`
	Error     string    `json:"error,omitempty"`
}

type updater struct {
	api, current, goos, goarch string
	dataDir, dir, exe          string // dir = DATA/update, exe = binario in uso
	pub                        ed25519.PublicKey
	hc                         *http.Client // TLS normale, NON legato all'impronta del NAS
	logf                       func(format string, a ...any)

	checkMu    sync.Mutex // un solo controllo alla volta
	mu         sync.Mutex
	st         updateState
	stagedPath string
}

func newUpdateHTTPClient() *http.Client { return &http.Client{Timeout: 5 * time.Minute} }

func newUpdater(dataDir, current string, logf func(string, ...any)) (*updater, error) {
	pk, err := hex.DecodeString(releasePubKeyHex)
	if err != nil || len(pk) != ed25519.PublicKeySize {
		return nil, errors.New("chiave pubblica dei rilasci non valida")
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	api := defaultUpdateAPI
	if v := os.Getenv("ENCLUCA_UPDATE_API"); v != "" {
		api = v
	}
	dir := filepath.Join(dataDir, "update")
	os.RemoveAll(dir) // resti di controlli precedenti
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &updater{
		api: api, current: current, goos: runtime.GOOS, goarch: runtime.GOARCH,
		dataDir: dataDir, dir: dir, exe: exe, pub: ed25519.PublicKey(pk),
		hc: newUpdateHTTPClient(), logf: logf,
		st: updateState{Current: current},
	}, nil
}

func (u *updater) state() updateState {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.st
}

func httpGet(ctx context.Context, hc *http.Client, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "encluca/"+Version)
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	return resp, nil
}

// fetchLatest legge versione e file dell'ultima release pubblicata.
func fetchLatest(ctx context.Context, hc *http.Client, api, current string) (string, map[string]string, error) {
	resp, err := httpGet(ctx, hc, api)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	var rel struct {
		Tag    string `json:"tag_name"`
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxAPISize)).Decode(&rel); err != nil {
		return "", nil, err
	}
	if _, ok := parseVersion(rel.Tag); !ok {
		return "", nil, fmt.Errorf(T("versione dell'ultima release non riconosciuta: %q"), rel.Tag)
	}
	assets := map[string]string{}
	for _, a := range rel.Assets {
		assets[a.Name] = a.URL
	}
	return strings.TrimPrefix(rel.Tag, "v"), assets, nil
}

func readLimited(ctx context.Context, hc *http.Client, url string, max int64) ([]byte, error) {
	resp, err := httpGet(ctx, hc, url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, errors.New(T("file dell'aggiornamento troppo grande"))
	}
	return b, nil
}

// download scrive url in path (al massimo max byte) e ne restituisce lo sha256 hex.
func download(ctx context.Context, hc *http.Client, url, path string, max int64) (string, error) {
	resp, err := httpGet(ctx, hc, url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, max+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	if n > max {
		return "", errors.New(T("file dell'aggiornamento troppo grande"))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// checkBinName: su Windows il programma da provare deve avere l'estensione .exe.
func checkBinName(goos string) string {
	if goos == "windows" {
		return "encluca-check.exe"
	}
	return "encluca-check.tmp"
}

func exeFileName(goos string) string {
	if goos == "windows" {
		return "encluca-new.exe"
	}
	return "encluca-new"
}

// canReplace verifica di poter scrivere nella cartella del programma.
func canReplace(exe string) error {
	f, err := os.CreateTemp(filepath.Dir(exe), ".encluca-write-test-*")
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}

// check cerca una versione più nuova; se c'è, la scarica, la verifica e la
// prepara in u.dir. Non installa nulla.
func (u *updater) check(ctx context.Context) error {
	u.checkMu.Lock()
	defer u.checkMu.Unlock()
	v, err := u.prepare(ctx)
	u.mu.Lock()
	u.st.LastCheck = time.Now()
	if err != nil {
		u.st.Error = err.Error()
	} else {
		u.st.Error = ""
		if v != "" {
			u.st.Available = v
		}
	}
	u.mu.Unlock()
	return err
}

func (u *updater) prepare(ctx context.Context) (string, error) {
	if err := canReplace(u.exe); err != nil {
		return "", fmt.Errorf(T("non posso sostituire il programma (%s): aggiornamento automatico non disponibile, aggiorna a mano"), filepath.Dir(u.exe))
	}
	v, assets, err := fetchLatest(ctx, u.hc, u.api, u.current)
	if err != nil {
		return "", err
	}
	if !newerVersion(v, u.current) {
		return "", nil
	}
	u.mu.Lock()
	staged := u.st.Staged
	u.mu.Unlock()
	if staged == v {
		return v, nil
	}
	tmpArchive := filepath.Join(u.dir, "archive.tmp")
	tmpBin := filepath.Join(u.dir, checkBinName(u.goos))
	ok := false
	defer func() {
		os.Remove(tmpArchive)
		if !ok {
			os.Remove(tmpBin)
		}
	}()
	sumsURL, sigURL := assets["SHA256SUMS"], assets["SHA256SUMS.sig"]
	if sumsURL == "" || sigURL == "" {
		return "", errors.New(T("il rilascio non è firmato (mancano SHA256SUMS o SHA256SUMS.sig)"))
	}
	sums, err := readLimited(ctx, u.hc, sumsURL, maxSumsSize)
	if err != nil {
		return "", err
	}
	sig, err := readLimited(ctx, u.hc, sigURL, maxSigSize)
	if err != nil {
		return "", err
	}
	hashes, err := verifySums(sums, sig, u.pub)
	if err != nil {
		return "", err
	}
	name := assetName(v, u.goos, u.goarch)
	want, inSums := hashes[name]
	url := assets[name]
	if !inSums || url == "" {
		return "", fmt.Errorf(T("il rilascio non contiene il pacchetto per questo sistema (%s)"), name)
	}
	got, err := download(ctx, u.hc, url, tmpArchive, maxArchiveSize)
	if err != nil {
		return "", err
	}
	if got != want {
		return "", errors.New(T("il pacchetto scaricato non corrisponde all'impronta firmata"))
	}
	if err := extractBinary(tmpArchive, v, u.goos, u.goarch, tmpBin); err != nil {
		return "", err
	}
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	out, err := exec.CommandContext(rctx, tmpBin, "version").Output()
	cancel()
	if err != nil || strings.TrimSpace(string(out)) != "Encluca "+v {
		return "", fmt.Errorf(T("il nuovo programma non risponde come la versione %s"), v)
	}
	final := filepath.Join(u.dir, exeFileName(u.goos))
	if err := os.Rename(tmpBin, final); err != nil {
		return "", err
	}
	ok = true
	u.mu.Lock()
	u.st.Staged, u.stagedPath = v, final
	u.mu.Unlock()
	u.logf(T("Aggiornamento disponibile: %s (scaricato e verificato, si installa alla chiusura della cassaforte)"), v)
	return v, nil
}

// ---- installazione ----

var errUpdateRestart = errors.New("riavvio per aggiornamento")

const updateExitCode = 75

// copyFileSync copia src in dst (sovrascrivendolo) e lo scrive su disco.
func copyFileSync(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, mode)
}

// cleanupOldExe rimuove i resti di un'installazione precedente.
func cleanupOldExe(exe string) {
	os.Remove(exe + ".old")
	os.Remove(exe + ".new")
}

func (u *updater) discardStaged() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.stagedPath != "" {
		os.Remove(u.stagedPath)
	}
	u.st.Staged, u.stagedPath = "", ""
}

// failInstall scarta il pacchetto pronto e riporta l'errore nello stato.
func (u *updater) failInstall(err error) {
	u.discardStaged()
	u.mu.Lock()
	u.st.Error = err.Error()
	u.mu.Unlock()
}

// installedVersion è la versione del programma su disco (può essere più nuova
// di quella in esecuzione se è stato aggiornato a mano); "" se non si legge.
func (u *updater) installedVersion() string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, u.exe, "version").Output()
	if err != nil {
		return ""
	}
	v, ok := strings.CutPrefix(strings.TrimSpace(string(out)), "Encluca ")
	if !ok {
		return ""
	}
	return v
}

// install sostituisce il programma con la versione pronta. Va chiamata solo a
// cassaforte chiusa (vedi Client.tryBeginUpdate).
func (u *updater) install() (from, to string, err error) {
	u.mu.Lock()
	staged, path := u.st.Staged, u.stagedPath
	u.mu.Unlock()
	if staged == "" {
		return "", "", errors.New(T("nessun aggiornamento pronto"))
	}
	inUse := u.current
	if v := u.installedVersion(); newerVersion(v, inUse) {
		inUse = v
	}
	if !newerVersion(staged, inUse) {
		u.discardStaged()
		return "", "", fmt.Errorf(T("l'aggiornamento pronto (%s) non è più nuovo della versione installata (%s): scartato"), staged, inUse)
	}
	if err := replaceExe(u.exe, path); err != nil {
		return "", "", err
	}
	rec := map[string]any{"from": u.current, "to": staged, "at": time.Now()}
	if err := saveJSON(filepath.Join(u.dataDir, "update-installed.json"), rec); err != nil {
		u.logf("update-installed.json: %v", err)
	}
	u.discardStaged()
	return u.current, staged, nil
}

// listenRetry ritenta l'ascolto: dopo un riavvio per aggiornamento la porta
// può restare occupata per qualche istante dal processo precedente.
func listenRetry(addr string, tries int, wait time.Duration) (net.Listener, error) {
	var err error
	for i := 0; i < tries; i++ {
		var ln net.Listener
		if ln, err = net.Listen("tcp", addr); err == nil {
			return ln, nil
		}
		if i < tries-1 {
			time.Sleep(wait)
		}
	}
	return nil, err
}

// ---- integrazione nel client ----

// tryBeginUpdate segna l'inizio di un'installazione, solo a cassaforte chiusa
// (anche non "rimasta aperta") e senza operazioni in corso.
func (c *Client) tryBeginUpdate() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cip != nil || c.st.Open || c.busy != "" || c.updating {
		return false
	}
	c.updating = true
	return true
}

func (c *Client) tryInstallUpdate() {
	staged := c.upd.state().Staged
	if staged == "" || !c.tryBeginUpdate() {
		return
	}
	c.log.Printf(T("Installo l'aggiornamento %s e riavvio il servizio"), staged)
	if _, _, err := c.upd.install(); err != nil {
		// si scarta il pacchetto: il prossimo controllo (6 h) lo riscarica da capo,
		// invece di ritentare ogni 30 s per sempre
		c.upd.failInstall(err)
		c.mu.Lock()
		c.updating = false
		c.mu.Unlock()
		c.log.Printf(T("Installazione dell'aggiornamento non riuscita: %v"), err)
		return
	}
	// updating resta vero: la cassaforte non si apre più fino al riavvio
	select {
	case c.restart <- struct{}{}:
	default:
	}
}

const (
	updateCheckEvery   = 6 * time.Hour
	updateInstallEvery = 30 * time.Second
)

// updateCycle controlla (se attivo) e prova a installare.
func (c *Client) updateCycle(ctx context.Context) {
	if !c.autoUpdateOn() {
		return
	}
	if err := c.upd.check(ctx); err != nil && ctx.Err() == nil {
		c.log.Printf(T("Controllo degli aggiornamenti non riuscito: %v"), err)
	}
	c.tryInstallUpdate()
}

func (c *Client) updateLoop(ctx context.Context) {
	checkT := time.NewTimer(0)
	defer checkT.Stop()
	inst := time.NewTicker(updateInstallEvery)
	defer inst.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-checkT.C:
			c.updateCycle(ctx)
			checkT.Reset(updateCheckEvery)
		case <-inst.C:
			if c.autoUpdateOn() {
				c.tryInstallUpdate()
			}
		}
	}
}

func (c *Client) autoUpdateOn() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cfg.AutoUpdate == nil || *c.cfg.AutoUpdate
}

func (c *Client) SetAutoUpdate(on bool) error {
	c.mu.Lock()
	c.cfg.AutoUpdate = &on
	err := saveJSON(c.cfgPath(), &c.cfg)
	c.mu.Unlock()
	if err != nil {
		return err
	}
	if on {
		c.log.Print(T("Aggiornamenti automatici attivati"))
	} else {
		c.log.Print(T("Aggiornamenti automatici disattivati"))
	}
	return nil
}

// updateStatusLocked è lo stato per l'interfaccia (c.mu già preso).
func (c *Client) updateStatusLocked() updateState {
	if c.upd == nil {
		return updateState{Current: Version, Error: T("aggiornamento automatico non disponibile su questa installazione")}
	}
	st := c.upd.state()
	st.Enabled = c.cfg.AutoUpdate == nil || *c.cfg.AutoUpdate
	return st
}

// ---- avviso sull'hub ----

// checkLatest legge solo il numero dell'ultima versione pubblicata (niente download).
func (h *Hub) checkLatest(ctx context.Context, hc *http.Client, api string) {
	v, _, err := fetchLatest(ctx, hc, api, Version)
	h.mu.Lock()
	defer h.mu.Unlock()
	if err != nil {
		h.latest = ""
		if !h.latestFail && ctx.Err() == nil {
			h.latestFail = true
			h.log.Printf(T("Controllo delle nuove versioni non riuscito: %v"), err)
		}
		return
	}
	h.latestFail = false
	if newerVersion(v, Version) && v != h.latest {
		h.log.Printf(T("Nuova versione disponibile: %s (aggiorna da TrueNAS: Apps → encluca → Stop, poi Start)"), v)
	}
	h.latest = v
}

func (h *Hub) latestLoop(ctx context.Context) {
	api := defaultUpdateAPI
	if v := os.Getenv("ENCLUCA_UPDATE_API"); v != "" {
		api = v
	}
	hc := newUpdateHTTPClient()
	for {
		h.checkLatest(ctx, hc, api)
		select {
		case <-ctx.Done():
			return
		case <-time.After(updateCheckEvery):
		}
	}
}
