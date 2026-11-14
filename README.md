<p align="center"><img src="assets/logo.svg" width="112" alt="encluca"></p>

# encluca

**Una cartella personale cifrata, sincronizzata tra i tuoi computer tramite il NAS, con backup cifrato su Google Drive.**

Un solo programma per Linux, macOS, Windows e TrueNAS. Niente componenti da installare a parte, niente versioni da allineare.

```
  PC (Linux / macOS / Windows)          NAS (TrueNAS)                 Google Drive
  ────────────────────────────          ─────────────                 ────────────
  "Encluca - APERTA"                    solo dati cifrati             copia cifrata
   (in chiaro solo quando la apri)  ⇄   + revisioni dei file     →    + versioni precedenti
  copia locale cifrata                  + dispositivi autorizzati
```

## Perché

- **La password non lascia mai i tuoi computer.** Il NAS e Google vedono solo dati cifrati, compresi i nomi di file e cartelle.
- **Decifri solo quando serve.** Apri la cassaforte, lavori nella cartella `Encluca - APERTA`, la chiudi. A quel punto sul disco resta solo il cifrato.
- **Funziona anche senza NAS.** Ogni modifica viene cifrata subito sul PC e inviata al NAS appena torna raggiungibile. Gli altri computer si allineano da soli.
- **Nessuna modifica va persa.** Se due PC modificano lo stesso file mentre sono scollegati, una delle due versioni viene salvata come copia `(conflitto …)`.
- **Backup che non replica gli errori.** Su Google Drive i file modificati o cancellati finiscono in `_versioni/<data>` invece di sparire.
- **Formato aperto.** I file sono nel formato di [rclone crypt](https://rclone.org/crypt/): anche senza encluca puoi sempre recuperarli con `rclone`.

## Installazione

Scarica il pacchetto per il tuo sistema dalla [pagina delle release](https://github.com/vonausterliz/encluca-releases/releases/latest).

| Dove | Pacchetto | Guida |
|---|---|---|
| NAS TrueNAS CE | immagine `ghcr.io/vonausterliz/encluca` (aggiornamento: *Stop* + *Start*) | [docs/INSTALLA-TRUENAS.md](docs/INSTALLA-TRUENAS.md) |
| PC Linux | `encluca-…-linux-amd64.tar.gz` | `./install.sh` |
| Mac Apple Silicon / Intel | `encluca-…-darwin-arm64` / `-darwin-amd64` | `./install.sh` |
| Windows | `encluca-…-windows-amd64.zip` | `install.ps1` |

In breve:
1. **NAS.** Installa l'hub seguendo la [guida per TrueNAS](docs/INSTALLA-TRUENAS.md). Sui server Linux generici puoi usare `./install.sh hub`.
2. **Ogni PC.** Installa il client, che parte come servizio in background. Apri http://127.0.0.1:8484 e incolla il codice di abbinamento generato dal NAS.
3. **Primo PC.** Crea la cassaforte con la tua password. Sugli altri PC aprila con la stessa password.
4. **Google Drive** (facoltativo). Collegalo dall'interfaccia del NAS.

**Aggiornamenti:** dalla 0.2.0 i computer si aggiornano da soli, solo con rilasci firmati e solo a cassaforte chiusa (si può disattivare dall'interfaccia). La prima 0.2.0 va installata a mano. L'hub si aggiorna da TrueNAS: Apps → encluca → Stop, poi Start. Come si pubblica un rilascio firmato: [docs/RILASCIO.md](docs/RILASCIO.md).

I dettagli d'uso, la sicurezza, i limiti e il recupero d'emergenza sono nel [LEGGIMI](LEGGIMI.md).

> ⚠️ **La password della cassaforte non è recuperabile.** Se la perdi, i dati restano illeggibili per sempre. Conservala in un password manager.

## Stato del progetto

Versione 0.1: funzionante e testata in automatico, ma ancora giovane.

| | |
|---|---|
| ✅ Verificato | cifratura compatibile con rclone crypt (test con il binario rclone); test completo con hub e due PC: propagazione, NAS spento, conflitti, cancellazioni, nessun dato in chiaro sul NAS |
| ⏳ Da verificare su sistemi reali | backup Google Drive, script di installazione per macOS e Windows, installazione su TrueNAS |

## Sviluppo

Serve Go 1.22 o superiore.

```bash
go test ./...   # test di cifratura (con rclone, se installato) e test end-to-end
./build.sh      # pacchetti per tutti i sistemi in dist/
```

Il repository è pubblico: prima del primo commit attiva il controllo dei segreti, che blocca
chiavi, token e password nei file preparati (lo stesso controllo gira anche su GitHub a ogni push):

```bash
GOBIN=$HOME/.local/bin go install github.com/zricethezav/gitleaks/v8@v8.30.1
git config core.hooksPath .githooks
```

| File | Ruolo |
|---|---|
| `internal/rcrypt/` | cifratura nel formato rclone crypt (contenuti: XSalsa20-Poly1305; nomi: AES-EME; chiavi: scrypt) |
| `hub.go` | server sul NAS: archivio cifrato, revisioni, dispositivi, interfaccia di amministrazione |
| `client.go` | servizio sul PC: copia locale cifrata, apertura/chiusura, sincronizzazione e conflitti |
| `gdrive.go` | backup versionato su Google Drive (API REST, scope `drive.file`) |
| `web/` | interfacce (incluse nel binario) |
| `packaging/` | script di installazione come servizio per ogni sistema |
