// Command spikes402 is the Spikes402 reverse proxy: put it in front of a
// website and protected paths will ask bots to pay sats or do work.
//
//	spikes402 -config config.json
package main

import (
	"crypto/rand"
	"flag"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/Choffolo/spikes402/client"
	"github.com/Choffolo/spikes402/internal/config"
	"github.com/Choffolo/spikes402/internal/gate"
	"github.com/Choffolo/spikes402/internal/lightning"
	"github.com/Choffolo/spikes402/pow"
)

func main() {
	configFile := flag.String("config", "config.json", "path to the JSON configuration file")
	flag.Parse()

	cfg, err := config.Load(*configFile)
	if err != nil {
		log.Fatal(err)
	}

	origin, _ := url.Parse(cfg.Origin) // validated by config.Load
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(origin)
			r.SetXForwarded()
		},
	}

	secret := []byte(cfg.SecretKey)
	if len(secret) == 0 { // only allowed with the fake backend
		secret = make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			log.Fatal(err)
		}
		log.Print("no secret_key set: using a random one (tokens will not survive a restart)")
	}

	mux := http.NewServeMux()
	var backend lightning.Backend
	switch cfg.Lightning.Backend {
	case "fake":
		fake := lightning.NewFake()
		backend = fake
		mux.Handle(client.FakePayPath, fake.PayHandler())
		log.Print("WARNING: fake Lightning backend: for development only, invoices are not real")
	case "phoenixd":
		backend = &lightning.Phoenixd{URL: cfg.Lightning.PhoenixdURL, Password: cfg.Lightning.PhoenixdPassword}
	}

	mux.Handle("/", gate.New(gate.Options{
		PriceSats:     cfg.PriceSats,
		Protect:       cfg.Protect,
		PowDifficulty: cfg.PowDifficulty,
		ChallengeTTL:  cfg.ChallengeTTL(),
		SecretKey:     secret,
		Lightning:     backend,
		Next:          proxy,

		ChallengesPerMinute: cfg.ChallengesPerMinute,
		MaxPendingInvoices:  cfg.MaxPendingInvoices,
		ClientIPHeader:      cfg.ClientIPHeader,
	}))

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	log.Printf("spikes402 listening on %s, protecting %v of %s at %d sats or PoW difficulty %d (~%.1f s on one CPU core)",
		cfg.Listen, cfg.Protect, cfg.Origin, cfg.PriceSats, cfg.PowDifficulty, pow.ExpectedSeconds(cfg.PowDifficulty))
	log.Fatal(srv.ListenAndServe())
}
