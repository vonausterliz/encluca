//go:build js && wasm

// Modulo WebAssembly per sfogliare la cassaforte dalla pagina del NAS.
// La decifratura avviene nel browser con lo stesso codice di encluca (internal/rcrypt):
// password e chiave restano nella scheda e non vengono mai inviate al NAS.
//
// Compilazione: GOOS=js GOARCH=wasm go build -o web/vault.wasm ./cmd/vaultwasm
package main

import (
	"bytes"
	"encoding/base64"
	"syscall/js"

	"encluca/internal/rcrypt"
)

var cip *rcrypt.Cipher

// unlock(password, salt, checkBase64) -> "" se la password è corretta, altrimenti un messaggio.
func unlock(_ js.Value, a []js.Value) any {
	c, err := rcrypt.New(a[0].String(), a[1].String())
	if err != nil {
		return err.Error()
	}
	chk, err := base64.StdEncoding.DecodeString(a[2].String())
	if err != nil {
		return "metadati non validi"
	}
	if plain, err := c.Open(chk); err != nil || string(plain) != rcrypt.CheckPlain {
		return "password errata"
	}
	cip = c
	return ""
}

// name(percorsoCifrato) -> percorso in chiaro, oppure null se non decifrabile.
func name(_ js.Value, a []js.Value) any {
	if cip == nil {
		return nil
	}
	p, err := cip.DecryptPath(a[0].String())
	if err != nil {
		return nil
	}
	return p
}

// file(Uint8Array cifrato) -> Uint8Array in chiaro, oppure null se il contenuto non è valido.
func file(_ js.Value, a []js.Value) any {
	if cip == nil {
		return nil
	}
	enc := make([]byte, a[0].Get("length").Int())
	js.CopyBytesToGo(enc, a[0])
	var out bytes.Buffer
	if err := cip.DecryptStream(&out, bytes.NewReader(enc)); err != nil {
		return nil
	}
	res := js.Global().Get("Uint8Array").New(out.Len())
	js.CopyBytesToJS(res, out.Bytes())
	return res
}

func lock(js.Value, []js.Value) any {
	cip = nil
	return nil
}

func main() {
	js.Global().Set("enclucaVault", js.ValueOf(map[string]any{
		"unlock": js.FuncOf(unlock),
		"name":   js.FuncOf(name),
		"file":   js.FuncOf(file),
		"lock":   js.FuncOf(lock),
	}))
	select {}
}
