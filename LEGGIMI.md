# Encluca

Una cartella personale **cifrata**, sincronizzata tra i tuoi computer (Linux, macOS, Windows)
tramite il NAS, con backup cifrato e versionato su Google Drive. Un solo programma: `encluca`.

```
  PC (client)                         NAS (hub)                         Google Drive
  ─────────────                       ─────────                         ────────────
  cartella "Encluca - APERTA"       solo file cifrati                 copia cifrata
  (in chiaro solo quando la apri)  ⇄  + elenco revisioni           →    + _versioni/<data>
  copia locale cifrata                + dispositivi autorizzati          (scope drive.file)
```

- **La password non lascia mai i tuoi PC.** Il NAS e Google vedono solo dati cifrati, anche i nomi di file e cartelle.
- **Funziona offline.** Le modifiche vengono cifrate subito sul PC e inviate appena il NAS è raggiungibile.
- **Nessuna modifica va persa.** Se due PC cambiano lo stesso file mentre sono scollegati, una delle due versioni viene salvata come `nome (conflitto PC 2026-11-08 19.23.05).ext`.
- **Formato aperto.** I file cifrati sono nel formato di [rclone crypt](https://rclone.org/crypt/): anche senza Encluca puoi recuperarli con `rclone` (vedi in fondo).

## Come si usa ogni giorno

1. Apri **Encluca** (icona sul desktop o http://127.0.0.1:8484). La prima volta in un browser (e poi ogni 90 giorni) vedi per un attimo il messaggio "si apre solo dal tuo utente" e l'interfaccia compare in una nuova scheda: serve a impedire che un altro utente dello stesso computer comandi la tua cassaforte.
2. Inserisci la password e premi **Apri**: compare la cartella `Encluca - APERTA` nella tua home. La fascia diventa gialla, a indicare che i file sono in chiaro.
3. Lavora sui file normalmente. Ogni modifica viene cifrata e inviata entro circa 2 secondi. Anche le **cartelle vuote** vengono sincronizzate e salvate nel backup su Drive.
4. Quando hai finito premi **Chiudi la cassaforte**: le ultime modifiche vengono cifrate e la cartella in chiaro viene cancellata.

Se spegni o riavvii il PC con la cassaforte aperta, su Linux e macOS il servizio la chiude da solo. Su Windows, al riavvio, l'app ti mostra "Cassaforte rimasta aperta": inserisci la password per riprendere e poi chiudi.

### Da terminale (senza interfaccia web)

Con il servizio client in esecuzione puoi fare tutto anche da riga di comando:

```
encluca status            # stato (aggiungi --json per gli script)
encluca open              # chiede la password senza mostrarla
encluca close
encluca sync              # sincronizza subito
encluca pair [--name NOME] https://NAS:8443 CODICE
encluca create            # crea la cassaforte (prima volta)
encluca confirm-deletes             # conferma eliminazioni bloccate da inviare
encluca confirm-received-deletes    # conferma eliminazioni bloccate ricevute
```

Negli script la password si può passare da standard input (`encluca open < file-protetto`), mai come argomento. I comandi usano il file `cli-token.json` (permessi 0600) che il servizio scrive nella sua cartella dati all'avvio e cancella all'arresto.

## Installazione

### 1. NAS (TrueNAS Community Edition)

> Guida dettagliata passo per passo, con controlli e disinstallazione: [docs/INSTALLA-TRUENAS.md](docs/INSTALLA-TRUENAS.md).

1. Crea un **nuovo** dataset dedicato, ad esempio `/mnt/tank/encluca`, con la sottocartella `data`.
2. Assegnala all'utente `apps` (uid 568), senza `-R`: `chown 568:568 /mnt/tank/encluca/data`
3. Vai in **Apps → Discover Apps → ⋮ → Install via YAML** e incolla `packaging/truenas/docker-compose.yaml`, correggendo il percorso `/mnt/tank/...`.
   Il nome dell'app deve essere `encluca`, tutto minuscolo. Il file aggiunge anche il pulsante **Web UI** di TrueNAS.
   Se la porta 8443 è già usata da un'altra app, cambia il numero a sinistra (es. `"18443:8443"`) e, in `x-portals`, `port: 18443`.
4. Apri `https://IP-DEL-NAS:8443`. Il browser avviserà che il certificato è autofirmato: è normale, accetta. Accedi con la password iniziale, che trovi in `data/admin-password.txt` o nel registro dell'app. **Cambiala subito.**

**Aggiornare:** *Apps → encluca → Stop*, poi *Start*. L'app usa l'immagine `ghcr.io/vonausterliz/encluca:latest` e a ogni avvio scarica l'ultima versione.

> In alternativa, su qualunque server Linux: `./install.sh hub`.

Per raggiungere il NAS da fuori casa serve una VPN (WireGuard, Tailscale o la VPN del router).
Quando aggiungi un dispositivo, indica nel campo "Indirizzo NAS visto dai PC" l'indirizzo raggiungibile dai PC.

### 2. Ogni computer

| Sistema | Comando, dalla cartella del pacchetto |
|---|---|
| Linux   | `./install.sh` (servizio utente systemd) |
| macOS   | `./install.sh`: installa **Encluca.app** in Applicazioni (si avvia al login). Quando macOS chiede l'accesso alla **rete locale**, rispondi *Consenti*; se non lo chiede e il NAS risulta irraggiungibile ("no route to host"): Impostazioni → Privacy e sicurezza → Rete locale → attiva Encluca |
| Windows | tasto destro su `install.ps1` → *Esegui con PowerShell* (oppure `powershell -ExecutionPolicy Bypass -File install.ps1`) |

Poi:
1. Sul NAS: **Aggiungi dispositivo** → copia il codice `EL2-…` (vale una sola volta, entro 24 ore).
2. Sul PC: incolla il codice e premi **Collega**.
3. Sul primo PC: **Crea la cassaforte** scegliendo la password. Sugli altri PC: **Apri** con la stessa password.

Per mettere dentro i dati esistenti, apri la cassaforte e copia i file nella cartella `Encluca - APERTA`.

Disinstallazione: `./install.sh uninstall` (Linux e macOS) oppure `install.ps1 -Uninstall` (Windows). I dati cifrati restano dove sono.

### 3. Backup su Google Drive

L'API di Google Drive è gratuita e non richiede la fatturazione. L'unico limite è lo spazio del tuo Drive (15 GB con l'account gratuito).

1. Apri https://console.cloud.google.com e accedi con l'account Google che riceverà il backup.
2. In alto, nel selettore dei progetti, scegli **Nuovo progetto**, chiamalo `encluca` e crealo. Assicurati che sia selezionato.
3. *API e servizi → Libreria*: cerca **Google Drive API** e premi **Abilita**.
4. *API e servizi → Schermata consenso OAuth* (o *Google Auth Platform*):
   - nome dell'app `encluca`, la tua email come email di assistenza e di contatto;
   - tipo di utenti: **Esterno**;
   - poi in **Pubblico** premi **Pubblica app** e conferma lo stato **In produzione**.
     ⚠️ Se resta in "Test", Google revoca l'accesso dopo 7 giorni e il backup si ferma. Per lo scope `drive.file` non serve la verifica di Google.
5. *Client* (o *Credenziali → Crea credenziali → ID client OAuth*): tipo **TV e dispositivi con input limitato**, nome `encluca-nas`. Copia **Client ID** e **Client Secret**.
6. Nell'interfaccia del NAS, sezione **Backup su Google Drive**, incolla Client ID e Client Secret e premi **Collega Google Drive**.
7. Compaiono un indirizzo (`https://www.google.com/device`) e un codice. Aprilo da qualunque dispositivo, inserisci il codice, scegli il tuo account e consenti l'accesso. Google può mostrare "app non verificata": scegli *Avanzate → Vai a encluca*. È la tua app.
8. Dopo qualche secondo lo stato diventa **Attivo** e parte il primo backup. Su Drive compare la cartella `Encluca`.

- Dopo ogni modifica, il backup parte entro circa un minuto. In più c'è un controllo completo periodico.
- I file modificati o cancellati non vengono persi: la vecchia copia viene spostata in `_versioni/<data-ora>/` e conservata per il numero di giorni scelto (30 di default).
- L'app vede solo la propria cartella su Drive (scope `drive.file`), non il resto del tuo Drive.

### 4. Aggiornamenti

- **Computer:** dalla 0.2.0 il client si aggiorna da solo. Controlla le nuove versioni all'avvio e poi ogni 6 ore, e installa **solo rilasci firmati** (firma ed25519 verificata con la chiave incorporata nel programma), mai versioni più vecchie, e **solo a cassaforte chiusa**. Dopo l'installazione il servizio si riavvia da solo. Puoi disattivarlo in *Impostazioni → Aggiornamenti automatici*.
- **La prima 0.2.0 va installata a mano** su ogni computer, come le versioni precedenti: i client 0.1.x non sanno aggiornarsi.
- **Dalla 0.2.2 il NAS accetta solo computer con encluca 0.2.2 o successivo.** I computer con la 0.2.x si aggiornano da soli; quelli con la 0.1.x vanno reinstallati a mano. Nella pagina *Dispositivi* del NAS ogni computer mostra la sua versione, e quelli da aggiornare hanno l'etichetta "Da aggiornare".
- **NAS:** l'hub non si aggiorna da solo. Quando c'è una versione nuova, la sua pagina lo segnala. Per aggiornare: TrueNAS → Apps → encluca → **Stop**, poi **Start**.

## Togliere l'avviso del browser

Quando apri la pagina del NAS il browser avvisa che la connessione non è privata, perché il certificato del NAS non è firmato da un'autorità che conosce. Si può togliere, e conviene farlo: così, se un giorno l'avviso ricomparisse, sarebbe un vero segnale d'allarme.

Ordine consigliato:

1. Aggiorna il NAS e tutti i computer a encluca **0.2.6 o successivo**.
2. Nella pagina del NAS, in *Dispositivi*, aspetta che ogni computer mostri **"CA ricevuta"** (succede da solo al primo collegamento dopo l'aggiornamento).
3. In *Certificato* premi **Scarica encluca-ca.crt** e importalo nel browser o nel sistema di ogni computer da cui apri la pagina del NAS (le istruzioni sono nella stessa scheda). Controlla che l'impronta coincida con quella mostrata.
4. Controlla che in *Indirizzi del NAS* ci siano gli indirizzi con cui raggiungi il NAS (di solito sono già lì).
5. Premi **Attiva il nuovo certificato** e ricarica la pagina: l'avviso non compare più.

Se un computer non è pronto, il NAS te lo dice prima di attivare. Per tornare indietro in qualsiasi momento: **Torna al certificato autofirmato**; i computer continuano a funzionare in entrambi i casi, senza riabbinarli.

Il certificato importato vale solo per indirizzi di rete privata e nomi di rete locale (per esempio `192.168.x.x` o `nas.local`): anche se la sua chiave venisse rubata, non potrebbe essere usato per falsificare siti pubblici. Il certificato del server si rinnova da solo ogni anno.

## Versioni precedenti e ripristino

Quando modifichi un file, il NAS conserva (cifrata) la versione precedente: **5 versioni per file, per 90 giorni**. Puoi cambiare questi valori nella pagina del NAS, in *Impostazioni* (0 versioni = non conservare).

Se un virus o un errore rovina molti file, nella pagina del NAS apri **Ripristino → Riporta tutto com'era**, scegli data e ora, premi **Controlla** per vedere quanti file cambierebbero e poi **Riporta com'era**. I file modificati tornano alla versione di allora, quelli eliminati ricompaiono, quelli creati dopo restano. I computer ricevono tutto come normali modifiche, e l'operazione si può annullare ripetendola con un'ora di poco precedente.

Per un singolo file: in *File della cassaforte* (se hai attivato l'esplorazione) premi **Versioni** accanto al file e poi **Ripristina**.

Le versioni stanno solo sul NAS, non nel backup su Drive, e occupano spazio sul NAS: lo vedi in *Impostazioni*.

## ⚠️ La password

**Non è recuperabile.** Se la perdi, i dati sono illeggibili per sempre, sia sul NAS sia su Google Drive. Conservala in un password manager e, se vuoi, su carta in un posto sicuro.

## Cosa resta in chiaro, e quando

| Dove | Cassaforte chiusa | Cassaforte aperta |
|---|---|---|
| PC – cartella `Encluca - APERTA` | non esiste | **file in chiaro** |
| PC – copia locale (`mirror/`) | cifrato | cifrato |
| NAS | cifrato | cifrato |
| Google Drive | cifrato | cifrato |
| Rete | TLS + contenuto cifrato | TLS + contenuto cifrato |

Mentre la cassaforte è aperta, i programmi che usi (Office, anteprime, indicizzazione del sistema) possono creare copie temporanee o cache al di fuori della cartella: è un limite di qualunque soluzione "a cartella". Per ridurre il rischio, chiudi la cassaforte quando non ti serve e usa la cifratura del disco del sistema operativo (FileVault, BitLocker, LUKS).

Il NAS conosce soltanto: numero e dimensione dei file, date di modifica e struttura delle cartelle (con i nomi cifrati).

## Limiti di questa versione

- **Nomi molto lunghi.** Un nome di file o cartella oltre circa 140 byte (le lettere accentate e simboli come `’` contano 2-3 byte) non può essere cifrato: l'app lo segnala negli avvisi e non chiude la cassaforte finché non lo rinomini, così il file non va perso.
- **Un solo NAS.** I PC non si sincronizzano direttamente tra loro, ma passano sempre dal NAS.
- **Apertura.** All'apertura vengono decifrati tutti i file: con molti GB servono alcuni secondi o minuti.
- **Interfaccia nel browser.** L'interfaccia è una pagina web locale con un collegamento sul desktop; non c'è un'icona nella barra di sistema.
- **Avviso del browser.** Finché non importi il certificato del NAS e non attivi quello nuovo (vedi *Togliere l'avviso del browser*), il browser mostra un avviso quando apri la pagina del NAS. I PC con encluca non ne risentono: riconoscono il NAS da soli.

## Recupero d'emergenza senza Encluca

Ti servono la password e il valore `salt` contenuto in `encluca-vault.json`, che trovi nella cartella dati del NAS e su Google Drive.

```bash
rclone config create encluca-recupero crypt \
  remote=/percorso/della/cartella/vault \
  filename_encryption=standard directory_name_encryption=true \
  password="$(rclone obscure 'LA-TUA-PASSWORD')" \
  password2="$(rclone obscure 'IL-SALT')"
rclone copy encluca-recupero: ./recuperati
```

Per il backup su Drive, usa come `remote` un remote Google Drive di rclone, ad esempio `gd:Encluca/vault`.

## Dove stanno i file

| | Percorso |
|---|---|
| Dati client Linux | `~/.local/share/encluca/` |
| Dati client macOS | `~/Library/Application Support/Encluca/` |
| Dati client Windows | `%LOCALAPPDATA%\Encluca\` |
| Registro client | `encluca.log` nella cartella dati |
| Dati hub | `/data` nel container (= `/mnt/tank/encluca/data`) |

## Sviluppo

```bash
go test ./...      # include un test con il binario rclone, se installato, e un test completo con hub + 2 PC
./build.sh         # pacchetti per tutti i sistemi in dist/
```
