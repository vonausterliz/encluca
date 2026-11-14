package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// fakeDrive implementa in memoria la parte dell'API di Google Drive usata dal backup.
type fakeDrive struct {
	mu    sync.Mutex
	srv   *httptest.Server
	files map[string]*fakeFile
	next  int
	sess  map[string]*fakeFile // sessioni di upload resumable
	puts  int
	// onPut, se impostato, è chiamato all'arrivo di ogni contenuto (n = numero progressivo);
	// se restituisce false il file non viene creato (upload interrotto)
	onPut func(n int, r *http.Request) bool
}

type fakeFile struct {
	ID, Name, Mime string
	Parents        []string
	Data           []byte
	Trashed        bool
}

var (
	reParent  = regexp.MustCompile(`'([^']*)' in parents`)
	reName    = regexp.MustCompile(`name = '((?:[^'\\]|\\.)*)'`)
	reMimeEq  = regexp.MustCompile(`mimeType = '([^']*)'`)
	reMimeNeq = regexp.MustCompile(`mimeType != '([^']*)'`)
)

func newFakeDrive(t *testing.T) *fakeDrive {
	d := &fakeDrive{files: map[string]*fakeFile{}, sess: map[string]*fakeFile{}}
	d.srv = httptest.NewServer(http.HandlerFunc(d.serve))
	t.Cleanup(d.srv.Close)
	oldAPI, oldUp, oldTok := gDriveAPI, gUploadAPI, gTokenURL
	gDriveAPI, gUploadAPI, gTokenURL = d.srv.URL+"/drive/v3", d.srv.URL+"/upload/drive/v3", d.srv.URL+"/token"
	t.Cleanup(func() { gDriveAPI, gUploadAPI, gTokenURL = oldAPI, oldUp, oldTok })
	return d
}

func (d *fakeDrive) newID() string { d.next++; return fmt.Sprintf("id%04d", d.next) }

func (d *fakeDrive) serve(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	switch {
	case p == "/token":
		json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 3600})
	case r.Method == http.MethodPost && p == "/drive/v3/files": // cartella
		var m struct {
			Name, MimeType string
			Parents        []string
		}
		json.NewDecoder(r.Body).Decode(&m)
		d.mu.Lock()
		f := &fakeFile{ID: d.newID(), Name: m.Name, Mime: m.MimeType, Parents: m.Parents}
		d.files[f.ID] = f
		d.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]string{"id": f.ID})
	case r.Method == http.MethodGet && p == "/drive/v3/files": // elenco con q
		q := r.URL.Query().Get("q")
		out := []map[string]string{}
		d.mu.Lock()
		for _, f := range d.files {
			if m := reParent.FindStringSubmatch(q); m != nil && !contains(f.Parents, m[1]) {
				continue
			}
			if m := reName.FindStringSubmatch(q); m != nil && strings.NewReplacer(`\'`, `'`, `\\`, `\`).Replace(m[1]) != f.Name {
				continue
			}
			if m := reMimeEq.FindStringSubmatch(q); m != nil && f.Mime != m[1] {
				continue
			}
			if m := reMimeNeq.FindStringSubmatch(q); m != nil && f.Mime == m[1] {
				continue
			}
			if f.Trashed && strings.Contains(q, "trashed = false") {
				continue
			}
			out = append(out, map[string]string{"id": f.ID, "name": f.Name})
		}
		d.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"files": out})
	case strings.HasPrefix(p, "/drive/v3/files/"):
		id := strings.TrimPrefix(p, "/drive/v3/files/")
		d.mu.Lock()
		defer d.mu.Unlock()
		f := d.files[id]
		if f == nil {
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
			return
		}
		switch r.Method {
		case http.MethodGet:
			json.NewEncoder(w).Encode(map[string]any{"id": f.ID, "trashed": false})
		case http.MethodDelete:
			delete(d.files, id)
			w.WriteHeader(http.StatusNoContent)
		case http.MethodPatch: // spostamento o cestino
			var body struct {
				Trashed bool `json:"trashed"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			if body.Trashed {
				f.Trashed = true
				json.NewEncoder(w).Encode(map[string]string{"id": f.ID})
				return
			}
			add, rem := r.URL.Query().Get("addParents"), r.URL.Query().Get("removeParents")
			var ps []string
			for _, x := range f.Parents {
				if x != rem {
					ps = append(ps, x)
				}
			}
			f.Parents = append(ps, add)
			json.NewEncoder(w).Encode(map[string]string{"id": f.ID})
		}
	case r.Method == http.MethodPost && p == "/upload/drive/v3/files": // inizio upload resumable
		var m struct {
			Name    string
			Parents []string
		}
		json.NewDecoder(r.Body).Decode(&m)
		d.mu.Lock()
		sid := d.newID()
		d.sess[sid] = &fakeFile{Name: m.Name, Parents: m.Parents, Mime: "application/octet-stream"}
		d.mu.Unlock()
		w.Header().Set("Location", d.srv.URL+"/upload/session/"+sid)
	case r.Method == http.MethodPut && strings.HasPrefix(p, "/upload/session/"):
		data, err := io.ReadAll(r.Body)
		if err != nil {
			return
		}
		d.mu.Lock()
		d.puts++
		n, hook := d.puts, d.onPut
		d.mu.Unlock()
		if hook != nil && !hook(n, r) {
			return
		}
		d.mu.Lock()
		f := d.sess[strings.TrimPrefix(p, "/upload/session/")]
		f.ID, f.Data = d.newID(), data
		d.files[f.ID] = f
		d.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]string{"id": f.ID})
	case r.Method == http.MethodPatch && strings.HasPrefix(p, "/upload/drive/v3/files/"):
		id := strings.TrimPrefix(p, "/upload/drive/v3/files/")
		data, _ := io.ReadAll(r.Body)
		d.mu.Lock()
		if f := d.files[id]; f != nil {
			f.Data = data
		}
		d.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]string{"id": id})
	default:
		http.Error(w, "non gestito: "+r.Method+" "+p, http.StatusBadRequest)
	}
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// vaultCopies conta, per ogni nome, i file presenti nella cartella vault su Drive.
func (d *fakeDrive) vaultCopies(vaultID string) map[string]int {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := map[string]int{}
	for _, f := range d.files {
		if f.Mime != folderMime && contains(f.Parents, vaultID) {
			out[f.Name]++
		}
	}
	return out
}

func setupBackup(t *testing.T, nFiles int) (*testEnv, *Client, *fakeDrive) {
	drive := newFakeDrive(t)
	env := newTestEnv(t)
	pc := env.newClient("PC1")
	if err := pc.CreateVault("una password lunga e sicura"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < nFiles; i++ {
		writeWork(t, pc, fmt.Sprintf("file %02d.txt", i), fmt.Sprintf("contenuto %d", i))
	}
	waitFor(t, "upload al NAS", func() bool { return pendingCount(pc) == 0 && env.hubFiles() == nFiles })
	env.hub.mu.Lock()
	env.hub.cfg.GDrive = GDriveConfig{ClientID: "id", ClientSecret: "s", RefreshToken: "r"}
	env.hub.mu.Unlock()
	return env, pc, drive
}

func checkNoDuplicates(t *testing.T, d *fakeDrive, g *GDrive, want int) {
	t.Helper()
	copies := d.vaultCopies(g.st.VaultID)
	if len(copies) != want {
		t.Fatalf("su Drive ci sono %d file distinti nella cassaforte, attesi %d", len(copies), want)
	}
	for name, n := range copies {
		if n != 1 {
			t.Fatalf("doppione su Drive: %s presente %d volte", name, n)
		}
	}
}

// Arresto brusco: file caricati ma non registrati nello stato non devono diventare doppioni.
func TestBackupRecoversWithoutDuplicates(t *testing.T) {
	env, _, drive := setupBackup(t, 25)
	g := env.hub.gd
	g.runBackup(context.Background(), g.cfg())
	if g.lastErr != "" {
		t.Fatal(g.lastErr)
	}
	checkNoDuplicates(t, drive, g, 25)
	if !g.st.Clean {
		t.Fatal("giro completo non segnato come terminato regolarmente")
	}
	// la data dell'ultimo backup sopravvive al riavvio dell'hub
	if again := newGDrive(env.hub); again.lastOK.IsZero() || again.lastInfo == "" {
		t.Fatal("data o esito dell'ultimo backup persi al riavvio")
	}
	// simula un arresto brusco: gli ultimi 5 caricamenti non sono stati registrati
	n := 0
	for p := range g.st.Files {
		if n++; n <= 5 {
			delete(g.st.Files, p)
		}
	}
	g.st.Clean = false
	g.runBackup(context.Background(), g.cfg())
	if g.lastErr != "" {
		t.Fatal(g.lastErr)
	}
	checkNoDuplicates(t, drive, g, 25)
}

// Arresto ordinato durante il backup: il file in corso non viene creato, lo stato salvato
// corrisponde a ciò che è su Drive e il giro successivo completa senza doppioni.
func TestBackupGracefulStop(t *testing.T) {
	env, _, drive := setupBackup(t, 12)
	g := env.hub.gd
	ctx, cancel := context.WithCancel(context.Background())
	drive.onPut = func(n int, r *http.Request) bool {
		if n == 6 { // arresto mentre arriva il sesto file
			cancel()
			<-r.Context().Done()
			return false
		}
		return true
	}
	g.runBackup(ctx, g.cfg())
	if g.lastErr != "" {
		t.Fatalf("l'arresto è stato registrato come errore: %s", g.lastErr)
	}
	// lo stato su disco deve elencare esattamente i file presenti su Drive
	var saved gdState
	if err := loadJSON(g.statePath(), &saved); err != nil {
		t.Fatal(err)
	}
	onDrive := drive.vaultCopies(saved.VaultID)
	if len(saved.Files) != len(onDrive) || len(onDrive) == 0 {
		t.Fatalf("stato salvato: %d file, su Drive: %d", len(saved.Files), len(onDrive))
	}
	drive.onPut = nil
	g.runBackup(context.Background(), g.cfg())
	if g.lastErr != "" {
		t.Fatal(g.lastErr)
	}
	checkNoDuplicates(t, drive, g, 12)
}

// folder restituisce la cartella (non nel cestino) chiamata name dentro parent.
func (d *fakeDrive) folder(name, parent string) *fakeFile {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, f := range d.files {
		if f.Mime == folderMime && f.Name == name && contains(f.Parents, parent) && !f.Trashed {
			return f
		}
	}
	return nil
}

func TestGDriveBackupEmptyDir(t *testing.T) {
	env, _, drive := setupBackup(t, 1)
	tok := env.deviceToken("X")
	if code, body := env.apiCall(t, http.MethodPut, "/api/v1/file?p="+encY+"&base=0&dir=1", tok, "2", nil); code != 200 {
		t.Fatalf("put dir: %d %s", code, body)
	}
	g := env.hub.gd
	g.runBackup(context.Background(), g.cfg())
	if g.lastErr != "" {
		t.Fatal(g.lastErr)
	}
	if drive.folder(encY, g.st.VaultID) == nil {
		t.Fatal("cartella vuota assente su Drive")
	}
	if n := drive.vaultCopies(g.st.VaultID)[encY]; n != 0 {
		t.Fatalf("la cartella vuota è stata caricata come file (%d copie)", n)
	}
}

func TestGDriveBackupDirRemoved(t *testing.T) {
	env, _, drive := setupBackup(t, 1)
	tok := env.deviceToken("X")
	env.apiCall(t, http.MethodPut, "/api/v1/file?p="+encY+"&base=0&dir=1", tok, "2", nil)
	env.apiCall(t, http.MethodPut, "/api/v1/file?p="+encF+"&base=0&dir=1", tok, "2", nil)
	g := env.hub.gd
	g.runBackup(context.Background(), g.cfg())
	fy, ff := drive.folder(encY, g.st.VaultID), drive.folder(encF, g.st.VaultID)
	if fy == nil || ff == nil {
		t.Fatal("cartelle assenti su Drive")
	}
	// in encF compare un file su Drive (caricato fuori da encluca): quella cartella non va toccata
	drive.mu.Lock()
	drive.files["extra"] = &fakeFile{ID: "extra", Name: "x", Mime: "application/octet-stream", Parents: []string{ff.ID}}
	drive.mu.Unlock()
	env.apiCall(t, http.MethodDelete, "/api/v1/file?p="+encY+"&base=1", tok, "2", nil)
	env.apiCall(t, http.MethodDelete, "/api/v1/file?p="+encF+"&base=1", tok, "2", nil)
	g.runBackup(context.Background(), g.cfg())
	if g.lastErr != "" {
		t.Fatal(g.lastErr)
	}
	if drive.folder(encY, g.st.VaultID) != nil {
		t.Fatal("la cartella vuota eliminata è ancora su Drive")
	}
	if drive.folder(encF, g.st.VaultID) == nil {
		t.Fatal("una cartella con contenuto non deve finire nel cestino")
	}
	if g.st.Dirs[encY] || g.st.Dirs[encF] {
		t.Fatalf("Dirs: %v", g.st.Dirs)
	}
	if _, ok := g.st.Folders[g.st.VaultID+"|"+encY]; ok {
		t.Fatal("la cartella eliminata è ancora in Folders")
	}
}
