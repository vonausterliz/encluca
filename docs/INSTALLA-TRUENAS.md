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
- vede **solo** le due cartelle del suo dataset: `bin` in sola lettura e `data`.

Per tornare indietro basta eliminare l'app e il dataset: vedi [Disinstallare](#disinstallare).

> ⚠️ **`NOMEPOOL` è un segnaposto: va sostituito ovunque**, sia nei comandi della Shell sia nel YAML, con il nome reale del tuo pool (vedi 0.1). Lo stesso vale per `IP-DEL-NAS`.
> Se nel YAML resta `NOMEPOOL`, Docker crea da solo una cartella vuota `/mnt/NOMEPOOL` e l'app si ferma con `stat /app/encluca: no such file or directory`. Vedi [Problemi comuni](#problemi-comuni).

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

## Passo 2 – Scarica il programma sul NAS

In *System → Shell* esegui un comando alla volta:

```
P=/mnt/NOMEPOOL/encluca
```
```
sudo mkdir "$P/bin" "$P/data"
```
Imposta la versione da installare (l'ultima è indicata nella [pagina delle release](https://github.com/vonausterliz/encluca/releases/latest)):
```
V=0.1.2
```
```
cd /tmp
curl -fLO https://github.com/vonausterliz/encluca/releases/download/v$V/encluca-$V-linux-amd64.tar.gz
curl -fLO https://github.com/vonausterliz/encluca/releases/download/v$V/SHA256SUMS.txt
```
Verifica che il file sia integro:
```
sha256sum -c --ignore-missing SHA256SUMS.txt
```
La risposta **deve** essere `encluca-…-linux-amd64.tar.gz: OK`. Se è diversa, fermati.

```
tar xzf encluca-$V-linux-amd64.tar.gz
sudo cp encluca-$V-linux-amd64/encluca "$P/bin/"
sudo chmod 755 "$P/bin/encluca"
```
Assegna all'utente `apps` **solo** le due cartelle nuove. Il comando è volutamente senza `-R`:
```
sudo chown 568:568 "$P/bin" "$P/data"
```
Controlla il risultato:
```
ls -ln "$P" "$P/bin"
```
Devi vedere `bin` e `data` con proprietario `568 568`, e il file `encluca` con permessi `-rwxr-xr-x`.

Pulisci i file temporanei:
```
rm -rf /tmp/encluca-$V-linux-amd64* /tmp/SHA256SUMS.txt
```

---

## Passo 3 – Installa l'app

1. Vai in *Apps → Discover Apps*.
2. Premi il menu **⋮**, in alto a destra accanto a *Custom App*, e scegli **Install via YAML**.
3. **Name**: `encluca`, solo lettere minuscole, numeri e trattini.
4. Nel riquadro **Custom Config** incolla il testo seguente, sostituendo `NOMEPOOL` nelle due righe `volumes`:

```yaml
services:
  encluca-hub:
    image: alpine:3.20
    container_name: encluca-hub
    command: ["/app/encluca", "hub", "--data", "/data", "--listen", ":8443"]
    user: "568:568"
    volumes:
      - /mnt/NOMEPOOL/encluca/bin:/app:ro
      - /mnt/NOMEPOOL/encluca/data:/data
    ports:
      - "8443:8443"
    restart: unless-stopped
    read_only: true
    security_opt:
      - no-new-privileges:true
```
Se al passo 0.3 la porta era occupata, la riga delle porte diventa `- "18443:8443"`.

5. Premi **Save**. TrueNAS scarica l'immagine `alpine:3.20`, di pochi MB, e avvia il container.
6. In *Apps → Installed*, dopo qualche secondo, `encluca` deve risultare **Running**.

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
Nel riquadro giallo *Cambia la password iniziale* inserisci la password attuale e una nuova di almeno 10 caratteri. Il file `admin-password.txt` viene cancellato automaticamente.

**4.4 Crea il codice per il primo PC**
Nella sezione *Dispositivi* scrivi un nome, ad esempio "PC Linux", e premi **Aggiungi dispositivo**. Copia il codice `EL1-…`: serve sul PC ed è mostrato una sola volta.

L'hub è pronto. Il seguito, cioè client e Google Drive, è descritto nel [LEGGIMI](../LEGGIMI.md).

---

## Problemi comuni

| Sintomo | Causa probabile | Soluzione |
|---|---|---|
| L'app resta in *Deploying* o va in *Crashed*, e nei log c'è `permission denied` | permessi delle cartelle | ripeti il `chown` del passo 2 e controlla con `ls -ln` |
| Nei log: `address already in use` | porta 8443 occupata | *Apps → encluca → Edit*, cambia la porta in `"18443:8443"` |
| `stat /app/encluca: no such file or directory` | nel YAML è rimasto `NOMEPOOL`, il percorso è sbagliato o il binario non è stato copiato | elimina l'app, rimuovi la cartella creata per errore con `sudo rmdir` (si rifiuta se non è vuota), correggi il percorso e reinstalla |
| Il browser non si connette | indirizzo o porta sbagliati | usa `https://` (non `http://`) e la porta scelta |

## Aggiornare a una nuova versione

Nella Shell di TrueNAS, sostituendo `NOMEPOOL` e indicando la nuova versione:
```
P=/mnt/NOMEPOOL/encluca
V=0.1.2
cd /tmp
curl -fLO https://github.com/vonausterliz/encluca/releases/download/v$V/encluca-$V-linux-amd64.tar.gz
curl -fLO https://github.com/vonausterliz/encluca/releases/download/v$V/SHA256SUMS.txt
sha256sum -c --ignore-missing SHA256SUMS.txt
```
Solo se la risposta è `OK`:
```
tar xzf encluca-$V-linux-amd64.tar.gz
sudo cp encluca-$V-linux-amd64/encluca "$P/bin/encluca"
rm -rf /tmp/encluca-$V-linux-amd64* /tmp/SHA256SUMS.txt
```
Poi in *Apps → encluca* premi **Restart**. Viene sostituito solo `bin/encluca`: `data`, cioè i file cifrati, i dispositivi e la password, resta intatto.

## Disinstallare

1. *Apps → Installed → encluca → Delete*.
2. *Datasets → encluca → Delete*. **Attenzione:** questo cancella anche la copia cifrata sul NAS. Fallo solo se i dati sono anche altrove, ad esempio nel backup su Google Drive, oppure non ti servono più.

Nient'altro sul NAS viene modificato.
