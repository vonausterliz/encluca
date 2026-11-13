package main

// Comandi da terminale per il client: parlano con il servizio "encluca client" già in esecuzione
// attraverso la stessa API locale usata dall'interfaccia web (127.0.0.1).
//
// Il servizio, all'avvio, scrive indirizzo e token della sessione in cli-token.json (permessi 0600)
// nella sua cartella dati: lo può leggere solo l'utente che esegue il servizio, cioè lo stesso
// confine di sicurezza della pagina web (che il browser mostra solo a chi la apre da questo PC).

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"
)

// cliCommands sono i sottocomandi gestiti da runCLI.
var cliCommands = map[string]bool{
	"status": true, "open": true, "close": true, "sync": true, "create": true, "pair": true,
	"confirm-deletes": true, "confirm-received-deletes": true,
}

type cliEndpoint struct {
	Addr  string `json:"addr"`
	Token string `json:"token"`
}

func cliTokenPath(dir string) string { return filepath.Join(dir, "cli-token.json") }

// writeCLIEndpoint salva indirizzo e token della sessione per i comandi da terminale.
func writeCLIEndpoint(dir, addr, token string) error {
	b, err := json.Marshal(cliEndpoint{addr, token})
	if err != nil {
		return err
	}
	p := cliTokenPath(dir)
	tmp := p + ".tmp"
	os.Remove(tmp)
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

type cliConn struct {
	ep   cliEndpoint
	http *http.Client
}

func cliConnect(dir string) (*cliConn, error) {
	b, err := os.ReadFile(cliTokenPath(dir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, errors.New(T("il servizio encluca client non è in esecuzione (avvialo prima)"))
		}
		return nil, err
	}
	var ep cliEndpoint
	if err := json.Unmarshal(b, &ep); err != nil || ep.Addr == "" || ep.Token == "" {
		return nil, errors.New(T("file del token non valido: riavvia il servizio encluca client"))
	}
	return &cliConn{ep: ep, http: &http.Client{Timeout: 10 * time.Minute}}, nil
}

// call esegue una chiamata all'API locale; body nil = GET.
func (cc *cliConn) call(path string, body any, out any) error {
	method := http.MethodGet
	var rd io.Reader
	if body != nil {
		method = http.MethodPost
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, "http://"+cc.ep.Addr+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("X-Encluca", "1")
	req.Header.Set("X-Encluca-Token", cc.ep.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := cc.http.Do(req)
	if err != nil {
		return fmt.Errorf(T("servizio encluca client non raggiungibile: %v"), err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error  string `json:"error"`
			Reload bool   `json:"reload"`
		}
		json.Unmarshal(raw, &e)
		if e.Reload {
			return errors.New(T("token non valido: riavvia il servizio encluca client"))
		}
		if e.Error == "" {
			e.Error = resp.Status
		}
		return errors.New(e.Error)
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// readPassword chiede la password senza mostrarla; se l'ingresso non è un terminale
// (es. encluca open < file) legge la prima riga.
func readPassword(prompt string) (string, error) {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		fmt.Fprint(os.Stderr, prompt)
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		return string(b), err
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", errors.New(T("nessuna password ricevuta"))
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func runCLI(cmd string, args []string) error {
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	data := fs.String("data", envOr("ENCLUCA_DATA", defaultClientDir()), "cartella dati del client")
	asJSON := fs.Bool("json", false, "stato in formato JSON (solo status)")
	name := fs.String("name", "", "nome di questo computer (solo pair)")
	fs.Parse(args)

	cc, err := cliConnect(*data)
	if err != nil {
		return err
	}
	var st map[string]any
	if err := cc.call("/api/status", nil, &st); err != nil {
		return err
	}
	if l, _ := st["lang"].(string); validLang(l) {
		setLang(l)
	}

	switch cmd {
	case "status":
		if *asJSON {
			delete(st, "log")
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(st)
		}
		printStatus(st)
		return nil
	case "open":
		if st["open"] == true {
			fmt.Println(T("La cassaforte è già aperta"))
			return nil
		}
		pw, err := readPassword(T("Password della cassaforte: "))
		if err != nil {
			return err
		}
		if err := cc.call("/api/open", map[string]string{"Password": pw}, nil); err != nil {
			return err
		}
		fmt.Println(T("Cassaforte aperta in"), st["workDir"])
	case "close":
		if st["open"] != true {
			fmt.Println(T("La cassaforte è già chiusa"))
			return nil
		}
		if err := cc.call("/api/close", map[string]string{}, nil); err != nil {
			return err
		}
		fmt.Println(T("Cassaforte chiusa"))
	case "create":
		pw, err := readPassword(T("Nuova password della cassaforte: "))
		if err != nil {
			return err
		}
		if term.IsTerminal(int(os.Stdin.Fd())) {
			pw2, err := readPassword(T("Ripeti la password: "))
			if err != nil {
				return err
			}
			if pw != pw2 {
				return errors.New(T("le due password non coincidono"))
			}
		}
		if err := cc.call("/api/create", map[string]string{"Password": pw}, nil); err != nil {
			return err
		}
		fmt.Println(T("Cassaforte creata"))
	case "sync":
		var res map[string]any
		if err := cc.call("/api/sync", map[string]string{}, &res); err != nil {
			return err
		}
		if res["online"] == true {
			fmt.Printf(T("NAS raggiungibile, file in coda: %v")+"\n", res["pending"])
		} else {
			fmt.Printf(T("NAS non raggiungibile, file in coda: %v")+"\n", res["pending"])
		}
	case "pair":
		if fs.NArg() != 2 {
			return errors.New(T("uso: encluca pair [--name NOME] URL-DEL-NAS CODICE"))
		}
		if err := cc.call("/api/pair", map[string]string{"HubURL": fs.Arg(0), "Code": fs.Arg(1), "Name": *name}, nil); err != nil {
			return err
		}
		fmt.Println(T("Computer collegato al NAS"))
	case "confirm-deletes":
		if err := cc.call("/api/confirm-deletes", map[string]string{}, nil); err != nil {
			return err
		}
		fmt.Println(T("Eliminazioni confermate: verranno inviate al NAS"))
	case "confirm-received-deletes":
		if err := cc.call("/api/confirm-recv-deletes", map[string]string{}, nil); err != nil {
			return err
		}
		fmt.Println(T("Eliminazioni ricevute confermate: verranno applicate su questo computer"))
	}
	return nil
}

func printStatus(st map[string]any) {
	yes := func(v any) string {
		if v == true {
			return T("sì")
		}
		return T("no")
	}
	str := func(v any) string {
		if v == nil {
			return "-"
		}
		if s, ok := v.(string); ok {
			if s == "" || strings.HasPrefix(s, "0001-01-01") {
				return "-"
			}
			if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
				return t.Local().Format("2006-01-02 15:04:05")
			}
			return s
		}
		return fmt.Sprint(v)
	}
	row := func(k string, v string) { fmt.Printf("%-24s %s\n", k, v) }
	row(T("Versione"), str(st["version"]))
	row(T("Collegato al NAS"), yes(st["paired"]))
	row(T("NAS"), str(st["hubUrl"]))
	row(T("Nome computer"), str(st["deviceName"]))
	row(T("NAS raggiungibile"), yes(st["online"]))
	row(T("Cassaforte aperta"), yes(st["open"]))
	row(T("Cartella"), str(st["workDir"]))
	row(T("File"), str(st["files"]))
	row(T("In coda"), str(st["pending"]))
	row(T("Ultima sincronizzazione"), str(st["lastSync"]))
	if b := str(st["busy"]); b != "-" {
		row(T("Operazione in corso"), b)
	}
	if e := str(st["lastError"]); e != "-" {
		row(T("Ultimo errore"), e)
	}
	if st["staleOpen"] == true {
		fmt.Println(T("ATTENZIONE: la cassaforte era rimasta aperta: aprila con la password e poi chiudila"))
	}
	if n, _ := st["massDelete"].(float64); n > 0 {
		fmt.Printf(T("ATTENZIONE: %d eliminazioni bloccate: encluca confirm-deletes per inviarle")+"\n", int(n))
	}
	if n, _ := st["massRecvDelete"].(float64); n > 0 {
		fmt.Printf(T("ATTENZIONE: %d eliminazioni ricevute bloccate: encluca confirm-received-deletes per applicarle")+"\n", int(n))
	}
	for _, k := range []string{"conflicts", "problems"} {
		if l, _ := st[k].([]any); len(l) > 0 {
			label := T("Conflitti")
			if k == "problems" {
				label = T("Problemi")
			}
			fmt.Printf("%s (%d):\n", label, len(l))
			for _, x := range l {
				fmt.Println("  -", x)
			}
		}
	}
	if l, _ := st["renames"].([]any); len(l) > 0 {
		fmt.Printf(T("%d nomi da rinominare: usa l'interfaccia web")+"\n", len(l))
	}
}
