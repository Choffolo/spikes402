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

I pagamenti arrivano al **tuo** nodo Lightning (phoenixd): Spikes402 non
custodisce mai chiavi né fondi di nessuno.

## Prova in 4 comandi (soldi finti)

Serve [Go](https://go.dev/dl/) 1.22 o successivo.

```sh
git clone https://github.com/Choffolo/spikes402 && cd spikes402
go run ./cmd/demo-origin &
go run ./cmd/spikes402 -config config.example.json &
go run ./cmd/spikes-bot -mode pow http://localhost:8402/premium/article
```

Con `-mode pay` il bot paga con un portafoglio finto invece di lavorare.

Licenza: [GNU AGPLv3](LICENSE).
