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

**Protezione dagli abusi:** ogni indirizzo IP può ricevere al massimo 30
risposte 402 al minuto (poi riceve "429, riprova più tardi"); chi richiede di
nuovo la stessa pagina riceve la stessa fattura invece di una nuova; e oltre
10.000 fatture aperte si offre solo la proof-of-work. Le pagine libere non
hanno limiti.

I pagamenti arrivano al **tuo** nodo Lightning (phoenixd): Spikes402 non
custodisce mai chiavi né fondi di nessuno.

## Prova in 4 comandi (soldi finti)

Serve [Go](https://go.dev/dl/) 1.22 o successivo.

```sh
git clone https://github.com/Choffolo/spikes402 && cd spikes402
go build -o bin/ ./cmd/...
bin/demo-origin & bin/spikes402 -config config.example.json & sleep 1
bin/spikes-bot -mode pow http://localhost:8402/premium/article
```

Con `-mode pay` il bot paga con un portafoglio finto invece di lavorare.

Licenza: [GNU AGPLv3](LICENSE).
