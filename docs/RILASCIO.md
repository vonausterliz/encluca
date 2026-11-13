# Come si pubblica un rilascio

Dalla 0.2.0 i client si aggiornano da soli, ma **solo** con rilasci firmati.
La firma è ed25519 sul file `SHA256SUMS`, che contiene l'impronta di ogni pacchetto.

## Chiave di firma

- Chiave privata: `~/.config/encluca-release/signing.key` (permessi 0600). **Non va mai nel repo.**
- Chiave pubblica: `update_key.go`, incorporata nel binario.
- Si genera una volta sola con `go run ./cmd/sign keygen`, che rifiuta di sovrascrivere una chiave esistente.
- Con `ENCLUCA_SIGNING_KEY=/percorso` si usa una chiave in un altro percorso.

**Fai una copia di backup della chiave privata** in un posto sicuro e non in cloud in chiaro, per esempio su una chiavetta cifrata o nel gestore di password.
Se la chiave si perde, i client già installati non accettano più aggiornamenti automatici.
Restano aggiornabili a mano: con una chiave nuova si pubblica una versione con la nuova chiave pubblica e la si installa a mano su ogni PC.
Se la chiave venisse rubata, chi l'ha potrebbe firmare aggiornamenti falsi: in quel caso disattiva gli aggiornamenti automatici sui PC e pubblica a mano una versione con una chiave nuova.

## Passi

1. Aggiorna `Version` in `main.go` e fai il commit.
2. `export GOTOOLCHAIN=go1.27.4 && ./build.sh`. Lo script si ferma se manca la chiave e produce in `dist/` i 5 pacchetti, `SHA256SUMS` e `SHA256SUMS.sig`.
3. Controllo: `(cd dist && sha256sum -c SHA256SUMS)` deve dare `OK` per tutti e 5.
4. Tag e push: `git tag vX.Y.Z && git push origin main vX.Y.Z`.
5. Release:
   ```
   gh release create vX.Y.Z dist/*.tar.gz dist/*.zip dist/SHA256SUMS dist/SHA256SUMS.sig --title "vX.Y.Z" --notes "…"
   ```
   **Senza `SHA256SUMS` e `SHA256SUMS.sig` i client non si aggiornano.** Non è un problema, ma l'aggiornamento automatico non parte.
6. Immagine Docker per l'hub: la costruisce il workflow di GitHub al push del tag. L'hub su TrueNAS si aggiorna a mano: Apps → encluca → Stop, poi Start.
