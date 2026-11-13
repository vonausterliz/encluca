# Installare l'hub encluca su TrueNAS Community Edition

Questa guida installa l'hub senza toccare nulla di ciò che hai già sul NAS.

## Cosa cambia sul NAS

| Viene creato | Non viene toccato |
|---|---|
| un nuovo dataset `encluca` | dataset, condivisioni SMB/NFS e snapshot esistenti |
| una nuova app `encluca` (un solo container, `encluca-hub`) | le altre app, gli utenti, la rete, gli aggiornamenti |

Il container:
- gira come utente `apps` (uid 568), non come root;
- ha il sistema di file in sola lettura e non può acquisire privilegi;
- vede **solo** la cartella `data` del suo dataset;
- usa l'immagine ufficiale `ghcr.io/vonausterliz/encluca`, compilata automaticamente dal codice pubblico su GitHub.

Per tornare indietro basta eliminare l'app e il dataset: vedi [Disinstallare](#disinstallare).

> ⚠️ **`NOMEPOOL` è un segnaposto: va sostituito ovunque**, sia nei comandi della Shell sia nel YAML, con il nome reale del tuo pool (vedi 0.1). Lo stesso vale per `IP-DEL-NAS`.
> Se nel YAML resta `NOMEPOOL`, Docker crea da solo una cartella vuota `/mnt/NOMEPOOL` e l'app non trova i suoi dati. Vedi [Problemi comuni](#problemi-comuni).

---

## Passo 0 – Controlli preliminari (non modificano nulla)

**0.1 Nome del pool**
Vai in *Storage*, oppure esegui nella Shell:
```
zpool list -H -o name
```
Scegli il pool dei dati, ad esempio `tank`. **Non** usare `boot-pool`, che è il pool di sistema.

**0.2 App già configurate**
Vai in *Apps*.
- Se vedi l'elenco delle app installate, o il pulsante *Discover Apps*, va bene.
- Se TrueNAS ti chiede di **scegliere un pool per le app**, fermati. Confermarlo crea il dataset di sistema `ix-apps`: è innocuo, ma è una modifica che decidi tu.

**0.3 Porta 8443 libera**
Vai in *System → Shell* ed esegui:
```
sudo ss -ltnp | grep -w 8443
```
`sudo` chiede la password del tuo utente amministratore (`truenas_admin` o quello che usi).
- Se il comando **non stampa nulla**, la porta è libera.
- Se stampa una riga, la porta è occupata. Al passo 3 userai `18443` e, al passo 4, l'indirizzo `https://IP-DEL-NAS:18443`.

**0.4 IP fisso del NAS (consigliato)**
I PC memorizzano l'indirizzo del NAS al momento dell'abbinamento. Se il NAS prende l'IP dal router (DHCP), conviene impostare sul router una prenotazione DHCP, così l'IP non cambia.

---

## Passo 1 – Crea il dataset

1. Vai in *Datasets*.
2. Nell'albero a sinistra seleziona la riga del pool, la prima in alto con il nome del pool.
3. Premi **Add Dataset**, in alto a destra.
4. Compila:
   - **Name**: `encluca`
   - **Dataset Preset**: `Generic`
   - lascia tutto il resto com'è, senza creare condivisioni SMB o NFS.
5. Premi **Save**.

Ora esiste `/mnt/NOMEPOOL/encluca`.

---

## Passo 2 – Prepara la cartella dati

In *System → Shell* esegui un comando alla volta:

```
P=/mnt/NOMEPOOL/encluca
```
```
sudo mkdir "$P/data"
```
`sudo` chiede la password del tuo utente amministratore. Assegna all'utente `apps` **solo** la cartella nuova, volutamente senza `-R`:
```
sudo chown 568:568 "$P/data"
```
Controlla il risultato:
```
ls -ln "$P"
```
Devi vedere `data` con proprietario `568 568`.

---

## Passo 3 – Installa l'app

1. Vai in *Apps → Discover Apps*.
2. Premi il menu **⋮**, in alto a destra accanto a *Custom App*, e scegli **Install via YAML**.
3. **Name**: `encluca`, solo lettere minuscole, numeri e trattini. Con una maiuscola (`Encluca`) il Save fallisce con `Error: app_name … String should match pattern`.
4. Nel riquadro **Custom Config** incolla il testo seguente, sostituendo `NOMEPOOL` nella riga `volumes`:

```yaml
services:
  encluca-hub:
    image: ghcr.io/vonausterliz/encluca:latest
    pull_policy: always
    container_name: encluca-hub
    user: "568:568"
    environment:
      TZ: Europe/Rome
    volumes:
      - /mnt/NOMEPOOL/encluca/data:/data
    ports:
      - "8443:8443"
    restart: unless-stopped
    read_only: true
    security_opt:
      - no-new-privileges:true

x-portals:
  - name: Web UI
    scheme: https
    host: 0.0.0.0
    port: 8443
    path: /
```
Se al passo 0.3 la porta era occupata, la riga delle porte diventa `- "18443:8443"` e in `x-portals` anche `port: 18443`.

5. Premi **Save**. TrueNAS scarica l'immagine di encluca (circa 15 MB) e avvia il container.
6. In *Apps → Installed*, dopo qualche secondo, `encluca` deve risultare **Running**.

`pull_policy: always` fa sì che a ogni avvio TrueNAS controlli se esiste una versione più recente: è quello che rende l'aggiornamento un solo clic.

Il blocco `x-portals` aggiunge il pulsante **Web UI** nel riquadro dell'app, come per le app del catalogo. `host: 0.0.0.0` significa "lo stesso indirizzo con cui stai usando TrueNAS". TrueNAS legge `x-portals` **solo quando l'app viene creata**: se lo aggiungi dopo con *Edit*, il pulsante non compare (vedi *Problemi comuni*).

---

## Passo 4 – Primo accesso

**4.1 Password iniziale**
Puoi recuperarla in due modi.
- *Apps → Installed → encluca*: nel riquadro *Workloads*, accanto al container `encluca-hub`, apri **View Logs**. Cerca la riga `Password amministratore iniziale: …`.
- Oppure dalla Shell:
  ```
  sudo cat /mnt/NOMEPOOL/encluca/data/admin-password.txt
  ```

**4.2 Apri l'interfaccia**
Vai su `https://IP-DEL-NAS:8443`.
Il browser avvisa che il certificato non è attendibile. È previsto: il certificato è autofirmato dall'hub. Scegli *Avanzate → Procedi*. I PC non si basano sul browser: verificano il NAS tramite l'impronta del certificato contenuta nel codice di abbinamento.

**4.3 Accedi e cambia subito la password**
Nel riquadro giallo *Cambia la password iniziale* inserisci la password attuale e una nuova di almeno 14 caratteri. Il file `admin-password.txt` viene cancellato automaticamente.

**4.4 Crea il codice per il primo PC**
Nella sezione *Dispositivi* scrivi un nome, ad esempio "PC Linux", e premi **Aggiungi dispositivo**. Copia il codice `EL2-…`: serve sul PC, è mostrato una sola volta, vale per un solo abbinamento e scade dopo 24 ore.

L'hub è pronto. Il seguito, cioè client e Google Drive, è descritto nel [LEGGIMI](../LEGGIMI.md).

---

## Problemi comuni

| Sintomo | Causa probabile | Soluzione |
|---|---|---|
| L'app resta in *Deploying* o va in *Crashed*, e nei log c'è `permission denied` | permessi della cartella `data` | ripeti il `chown` del passo 2 e controlla con `ls -ln` |
| Errore nello scaricare `ghcr.io/vonausterliz/encluca` | il NAS non raggiunge internet o GitHub | controlla DNS e gateway in *Network*; riprova con **Edit → Save** |
| Nei log: `address already in use` | porta 8443 occupata | *Apps → encluca → Edit*, cambia la porta in `"18443:8443"` |
| L'app si ferma con `permission denied` su `/data` oppure compare una cartella vuota `/mnt/NOMEPOOL` | nel YAML è rimasto `NOMEPOOL` o il percorso è sbagliato | elimina l'app, rimuovi la cartella creata per errore con `sudo rmdir` (si rifiuta se non è vuota), correggi il percorso e reinstalla |
| Il browser non si connette | indirizzo o porta sbagliati | usa `https://` (non `http://`) e la porta scelta |
| Al Save: `Error: app_name … String should match pattern` | il nome dell'app contiene maiuscole o caratteri non ammessi | scrivi `encluca`, tutto minuscolo |
| Manca il pulsante **Web UI** | `x-portals` aggiunto con *Edit* su un'app già esistente: TrueNAS lo ignora | *Apps → encluca → **Delete***, poi reinstalla con *Install via YAML*, stesso nome e stesso YAML. I dati stanno nella cartella del pool e non vengono toccati: i PC si ricollegano da soli |

## Aggiornare a una nuova versione

*Apps → encluca → **Stop***, attendi *Stopped*, poi **Start**. Alla ripartenza TrueNAS ricrea il container e, grazie a `pull_policy: always`, scarica l'ultima versione dell'immagine (*Restart* potrebbe riavviare il container esistente senza scaricarla). Ricarica la pagina dell'hub e controlla il numero di versione in alto a destra.

Se la versione non cambia, vai in *Apps → encluca → **Edit*** e premi **Save** senza modificare nulla: TrueNAS ricrea il container scaricando l'immagine.

La cartella `data`, cioè file cifrati, dispositivi, password e collegamento a Google Drive, resta intatta. Durante il riavvio i PC mettono le modifiche in coda e le inviano appena l'hub torna disponibile.

## Passare dalla vecchia installazione (cartella `bin`)

Se hai installato l'hub copiando il file `encluca` nella cartella `bin` (versioni fino alla 0.1.3), passa all'immagine una volta sola:

1. Apri *Apps → encluca → **Edit*** e copia in un file di testo il vecchio YAML: serve per tornare indietro. Annota il percorso della riga `…/encluca/data:/data`.
2. Chiudi senza salvare e premi *Apps → encluca → **Delete***. La cartella `data` sta nel pool e non viene toccata.
3. Reinstalla come al [passo 3](#passo-3--installa-lapp), con il nome `encluca` e il tuo percorso `data`. Reinstallare, invece di usare *Edit*, serve ad avere anche il pulsante **Web UI**. Nel nuovo YAML non deve restare la riga `…/encluca/bin:/app:ro`: coprirebbe il programma dell'immagine e partirebbe ancora la versione vecchia.
4. Ricarica la pagina dell'hub: dispositivi, password e Google Drive sono quelli di prima, e i PC si ricollegano senza un nuovo abbinamento.
5. Facoltativo, quando hai verificato che tutto funziona: la cartella `bin` non serve più.
   ```
   sudo rm -r /mnt/NOMEPOOL/encluca/bin
   ```

## Disinstallare

1. *Apps → Installed → encluca → Delete*.
2. *Datasets → encluca → Delete*. **Attenzione:** questo cancella anche la copia cifrata sul NAS. Fallo solo se i dati sono anche altrove, ad esempio nel backup su Google Drive, oppure non ti servono più.

Nient'altro sul NAS viene modificato.
