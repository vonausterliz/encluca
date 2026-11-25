# Correzioni di sicurezza: cosa succede e cosa fare

Questa pagina dice, per ogni pezzo di encluca, come arriva una correzione quando si trova una
vulnerabilità, chi deve fare qualcosa e in quanto tempo i dispositivi sono protetti.
I passi di pubblicazione sono in [RILASCIO.md](RILASCIO.md).

## Per componente

| Componente | Come arriva la correzione | Chi agisce | Tempo |
|---|---|---|---|
| **Client** sui PC (Linux, Windows, Mac), con **CLI** e **icona** (stesso file) | Release firmata: il client la controlla ogni 6 ore, verifica la firma, la installa e si riavvia. L'icona si rilancia da sola con la versione nuova | Nessuno, se l'aggiornamento automatico è attivo | Entro 6 ore, ma solo con la cassaforte chiusa |
| **Hub** sul NAS (immagine Docker) | Il tag della release fa pubblicare `ghcr.io/vonausterliz/encluca:latest` | Tu: TrueNAS → Apps → encluca → Stop, poi Start | Quando lo fai. Dalla 0.2.12 i computer mostrano l'avviso "aggiorna il NAS" |
| **Pagina web e cassaforte nel browser** (WASM) | È dentro il file dell'hub | Come l'hub | Come l'hub |
| **Immagine base** (Alpine) | Si ricostruisce a ogni release. Dependabot propone il passaggio alle versioni nuove di Alpine | Io: release. Tu: Stop/Start | Alla release successiva |
| **Go e moduli** (`x/crypto`, `x/sys`, `systray`, `dbus`) | `govulncheck` gira a ogni push e **ogni lunedì**. Dependabot propone gli aggiornamenti | Io: aggiornamento e release | Dopo la mail di GitHub o la PR di Dependabot |
| **GitHub Actions** (fissate a uno SHA) | Dependabot propone il nuovo SHA | Io | Alla PR di Dependabot |
| **Chiave di firma dei rilasci** | Una sola chiave, la pubblica è scritta nel programma | Io | Se si perde o viene rubata serve reinstallare a mano su ogni PC (vedi RILASCIO.md) |
| **CA dell'hub** | Pulsante "Rigenera la CA" nella pagina del NAS (vedi sotto) | Tu | Minuti, più la reimportazione nei browser |
| **Formato di cifratura** (rclone crypt) | Non si corregge con un aggiornamento: andrebbero ricifrati i dati | Tu + io | Lungo |

## Correzione nel client

1. Correggi, rilascia (RILASCIO.md).
2. Se la vulnerabilità è seria, alza `minClientVersion` in `hub.go` alla versione corretta, nella stessa release o nella successiva.
   Dopo l'aggiornamento del NAS, i computer più vecchi vengono rifiutati: vedono l'avviso "aggiornalo alla X o successiva" e nella pagina del NAS risultano "Da aggiornare".
   Chi ha l'aggiornamento automatico attivo si aggiorna da solo entro 6 ore, perché l'aggiornamento arriva da GitHub e non dal NAS.
   Chi lo ha spento resta fermo finché non lo aggiorna.
   Se aggiorni il NAS subito dopo la release, i computer restano fermi per qualche ora, finché non si aggiornano. Per evitarlo, aspetta qualche ora prima di aggiornare il NAS.
3. Aggiorna il NAS (Stop/Start).

## Correzione nell'hub

1. Correggi, rilascia.
2. TrueNAS → Apps → encluca → Stop, poi Start.
   Finché non lo fai, i computer (dalla 0.2.12) mostrano l'avviso "Il NAS usa encluca X, questo computer la Y".
   L'avviso compare solo quando il computer è più nuovo del NAS, cioè dopo che si è già aggiornato.

## Rigenerare la CA del NAS

Il pulsante cambia la CA che **i browser** usano per fidarsi del NAS.
La CA può firmare solo indirizzi privati e nomi di rete locale (name constraints), quindi anche rubata non vale per siti pubblici.

**Attenzione: da solo non protegge i computer.**
I computer si fidano anche del certificato autofirmato del NAS (`tls-key.pem`), che sta nella stessa cartella e che il pulsante non cambia.
Chi ha copiato i file potrebbe ancora fingersi il NAS verso i computer della tua rete.
Se pensi che qualcuno abbia copiato **l'intero dataset**, segui la procedura completa più sotto.

1. Pagina del NAS → Certificato → 4. "Rigenera la CA".
   Il NAS torna subito al certificato autofirmato: i computer continuano a sincronizzare senza riabbinarli.
2. Aspetta che nella scheda Certificato tutti i computer risultino pronti: ricevono la nuova CA al primo collegamento.
3. Nei browser e sui telefoni **rimuovi il vecchio "encluca-ca"** e importa quello nuovo (pulsante "Scarica encluca-ca.crt", stessi passi della prima volta). Controlla l'impronta.
4. Passo 3: "Attiva il nuovo certificato".
   Da questo momento i computer non accettano più la vecchia CA.

Un computer spento durante la rotazione risulta "non pronto": accendilo e aspetta che si colleghi prima di attivare.
Se attivi lo stesso (conferma forzata), quel computer smette di sincronizzare finché non torni al certificato autofirmato.
I codici di abbinamento creati prima della rotazione e non ancora usati non funzionano più dopo l'attivazione: generane uno nuovo.

### Procedura completa: qualcuno ha copiato i file del NAS

I file della cassaforte sono cifrati e la password non sta sul NAS, quindi il contenuto resta protetto.
Sul NAS ci sono però le chiavi dei certificati, l'impronta della password admin e l'autorizzazione a Google Drive.

1. TrueNAS → Apps → encluca → Stop.
2. Nel dataset (cartella `data`) cancella `tls-cert.pem`, `tls-key.pem`, `ca-cert.pem` e `ca-key.pem`. Poi Start: l'hub ne crea di nuovi.
3. I computer smettono di collegarsi: si fidavano del certificato vecchio, ed è proprio quello che si vuole. Su ognuno chiudi la cassaforte e abbinalo di nuovo con un codice nuovo dalla pagina del NAS. Poi elimina dalla pagina del NAS i dispositivi vecchi.
4. Cambia la password admin del NAS.
5. Revoca l'accesso di encluca a Google Drive (account Google → Sicurezza → App di terze parti) e autorizzalo di nuovo dalla pagina del NAS.
6. Nei browser e sui telefoni rimuovi il vecchio "encluca-ca" e importa quello nuovo, poi attivalo (passi 3-4 sopra).

## Mail di GitHub "workflow fallito" (controllo del lunedì)

- **govulncheck** segnala una vulnerabilità:
  - se riguarda la libreria standard di Go, aggiorna `toolchain` in `go.mod` e l'immagine `golang:` nel `Dockerfile`;
  - se riguarda un modulo, `go get modulo@versione && go mod tidy`;
  - poi test e release.
  Se govulncheck dice che il codice vulnerabile non viene chiamato, si può aspettare la release successiva.
- **gitleaks**: un segreto nel repository. Va revocato subito (token, password) e tolto dalla storia.
- **Test**: un test diventato instabile. Va controllato, ma non è un problema di sicurezza.

`govulncheck` è fissato a una versione nel workflow (`@v1.9.0`) e Dependabot non lo aggiorna. Il database delle vulnerabilità è sempre quello attuale, ma ogni tanto conviene alzare la versione a mano, soprattutto quando cambia la versione di Go.

GitHub sospende i workflow programmati dopo 60 giorni senza attività nel repository e lo segnala per mail.
Si riattivano da Actions → Test → "Enable workflow".

## Pull request di Dependabot

Una alla settimana al massimo per tipo (moduli Go, Actions, Docker).
Si controlla che la CI sia verde e si unisce. Gli aggiornamenti di sicurezza vanno poi in una release.

## Rimandati (da fare a fine progetto)

- Seconda chiave di firma di emergenza, con la chiave privata solo offline, e backup della chiave attuale.
- Immagine dell'hub `scratch` invece di Alpine: dentro solo encluca, certificati e fusi orari.
