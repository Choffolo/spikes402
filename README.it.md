# Spikes402 — Pay or PoW (riassunto in italiano)

**Spikes402** è un piccolo proxy da mettere davanti a un sito web. Sulle
pagine scelte dal titolare, i bot e i crawler delle intelligenze artificiali
devono **pagare** pochi satoshi su Lightning (standard L402) **oppure
lavorare**, risolvendo una proof-of-work (doppio SHA-256, come il mining di
Bitcoin in miniatura). Il resto del sito resta libero.

> **Stato: sperimentale.** È un primo prototipo: non usarlo con soldi veri
> prima di una revisione indipendente. Documentazione completa in [inglese](README.md).

## Come funziona, in breve

1. Il bot chiede una pagina protetta e riceve **HTTP 402** con una fattura
   Lightning e un indovinello di proof-of-work.
2. Paga la fattura (e riceve una "ricevuta", il *preimage*) **oppure** risolve
   l'indovinello.
3. Ripete la richiesta con la prova e riceve la pagina.
4. Ogni prova vale **una volta sola**, **per una sola pagina** e **scade**
   dopo 10 minuti.

**Quanto lavoro vale un sat?** Di default 1 sat equivale a circa 10 secondi
di lavoro di un core di CPU (impostazione `pow_seconds_per_sat`). Per esempio
1 sat ≈ 8 secondi, 10 sat ≈ 2 minuti; una scheda grafica fa circa 1000 volte
prima. La risposta 402 indica al bot il tempo previsto.

**Protezione del nodo Lightning:** la proof-of-work si offre sempre e non
costa quasi nulla al server; una fattura si crea solo entro limiti precisi.
Ogni indirizzo IP riceve al massimo 30 risposte 402 al minuto (poi "429,
riprova più tardi"); chi richiede la stessa pagina riceve la stessa fattura.
In totale si creano al massimo 300 fatture al minuto e ne restano aperte al
massimo 10.000. Ogni chiamata al nodo ha un timeout di 3 secondi, e se il nodo
sbaglia 5 volte di fila lo si lascia in pace per 30 secondi. Oltre questi
limiti si offre solo la proof-of-work. L'IP reale si legge da un header solo
se arriva da un proxy fidato (`trusted_proxies`). Le pagine libere non hanno
limiti.

**Proof-of-work Argon2id (opzionale):** con `"pow_algorithm": "argon2id"`
l'indovinello usa molta memoria (16 MiB di default) a ogni tentativo, così
schede grafiche e hardware specializzato hanno meno vantaggio rispetto a una
normale CPU. Il default resta il doppio SHA-256. Anche il server spende
memoria per controllare una soluzione, quindi controlla solo gettoni validi,
non scaduti e non usati, entro i limiti per IP e al massimo 4 alla volta. Una
risposta sbagliata brucia il gettone. RandomX è un'idea per il futuro.

**Chi è il bot?** Sulle pagine protette Spikes402 prova a riconoscere chi
chiede e lo scrive nei log e nell'header `Spikes402-Class`:

- `verified-signed`: il bot firma le richieste con **Web Bot Auth**
  (standard IETF in bozza: firme HTTP Ed25519 con le chiavi pubblicate dal
  bot sul suo sito);
- `verified`: un crawler noto (Googlebot, GPTBot, ClaudeBot…) confermato
  dalle liste di IP pubblicate dal gestore o dal DNS inverso;
- `impostor`: dice di essere un crawler noto ma l'indirizzo non è suo;
- `unknown`: tutti gli altri.

Dove la bozza lascia margini abbiamo scelto l'interpretazione più prudente:
firme valide al massimo 1 ora, ogni firma usabile una volta sola, solo https
e directory di chiavi scaricate con limiti di tempo e di dimensione.

**Di default tutti pagano lo stesso prezzo**, anche i bot verificati: se i
bot verificati debbano pagare meno, di più o niente è una **questione
aperta**. Per chi vuole sperimentare c'è l'opzione `bot_policies` (per
esempio "solo proof-of-work per gli impostori"), spenta di default.

**Pannello di controllo (opzionale).** Con una chiave admin
(`SPIKES402_ADMIN_KEY`) si apre una dashboard su `http://127.0.0.1:8404`,
visibile solo dal server stesso (da fuori con un tunnel SSH). Mostra
statistiche, stato del nodo Lightning e impostazioni, e permette di cambiare
al volo prezzo, proof-of-work, limiti e regole per i bot (valgono fino al
riavvio).

**Il monitor dell'assedio.** Una vista dal vivo delle mura: ogni richiesta a
una pagina protetta è un puntino che parte da sinistra ("la frontiera") e
corre verso il muro di energia davanti al sito. Il colore dice chi è: blu =
bot firmato, verde = crawler verificato, rosso = impostore, grigio =
sconosciuto. Al muro: ⚡ ha pagato e ⚙ ha fatto il lavoro, e passano dalla
porta; `402` si ferma (ha ricevuto il prezzo); `✗`, `429` e `403` vengono
respinti. In alto i contatori, sotto chi bussa e l'elenco degli ultimi
arrivi. Gli indirizzi dei visitatori sono anonimizzati.

**Assistente AI (MCP).** `spikes402-mcp` permette a un assistente AI di
leggere i dati e **proporre** modifiche, che applichi tu con un clic. Non può
mai prelevare fondi.

**Prelievi (opzionali, spenti di default).** Verso un solo indirizzo scritto
nel file di configurazione, con una password dedicata e una doppia conferma.
Richiedono la password *completa* di phoenixd: attivarli solo se servono.

I pagamenti arrivano al **tuo** nodo Lightning (phoenixd): Spikes402 non
custodisce mai chiavi né fondi di nessuno.

## Prova in 4 comandi (soldi finti)

Serve [Go](https://go.dev/dl/) 1.26 o successivo.

```sh
git clone https://github.com/Choffolo/spikes402 && cd spikes402
go build -o bin/ ./cmd/...
bin/demo-origin & bin/spikes402 -config config.example.json & sleep 1
bin/spikes-bot -mode pow http://localhost:8402/premium/article
```

Con `-mode pay` il bot paga con un portafoglio finto invece di lavorare. Con
`-sign` firma le richieste con Web Bot Auth e stampa la sua directory di chiavi.

Licenza: [GNU AGPLv3](LICENSE).
