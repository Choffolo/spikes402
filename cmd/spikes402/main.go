// Command spikes402 is the Spikes402 reverse proxy: put it in front of a
// website and protected paths will ask bots to pay sats or do work.
//
//	spikes402 -config config.json
package main

import (
	"context"
	"crypto/rand"
	"flag"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/Choffolo/spikes402/client"
	"github.com/Choffolo/spikes402/internal/admin"
	"github.com/Choffolo/spikes402/internal/botid"
	"github.com/Choffolo/spikes402/internal/config"
	"github.com/Choffolo/spikes402/internal/gate"
	"github.com/Choffolo/spikes402/internal/lightning"
	"github.com/Choffolo/spikes402/webbotauth"
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

	trusted, _ := cfg.TrustedProxyNets() // validated by config.Load

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

	// Bot identification: Web Bot Auth signatures first, then the IP
	// lists and reverse DNS published by the crawler operators.
	var classifier *botid.Classifier
	if cfg.IdentifyBotsOn() {
		if cfg.WebBotAuthDevMode {
			log.Print("WARNING: web_bot_auth_dev_mode: accepting key directories on http and local addresses, for development only")
		}
		classifier = botid.New(cfg.Bots(), webbotauth.NewVerifier(webbotauth.NewDirectories(cfg.WebBotAuthDevMode)))
		go classifier.Run(context.Background(), log.Printf)
	}

	// The admin dashboard, if enabled, watches every protected request.
	var monitor *admin.Monitor
	var observer func(gate.Event)
	if cfg.AdminEnabled() {
		monitor = admin.NewMonitor(cfg.Admin.VisitorIPs, time.Now())
		observer = monitor.Observe
	}

	g := gate.New(gate.Options{
		PriceSats:    cfg.PriceSats,
		Protect:      cfg.Protect,
		PoW:          cfg.PoW,
		ChallengeTTL: cfg.ChallengeTTL(),
		SecretKey:    secret,
		Lightning:    backend,
		Next:         proxy,

		ChallengesPerMinute:  cfg.ChallengesPerMinute,
		MaxInvoicesPerMinute: cfg.MaxInvoicesPerMinute,
		MaxPendingInvoices:   cfg.MaxPendingInvoices,
		ClientIPHeader:       cfg.ClientIPHeader,
		TrustedProxies:       trusted,

		Classifier: classifier,
		Policies:   cfg.BotPolicies,
		Observer:   observer,
	})
	mux.Handle("/", g)

	if cfg.AdminEnabled() {
		startAdmin(cfg, g, monitor, backend)
	}

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	p := cfg.PoW(cfg.PriceSats)
	log.Printf("spikes402 listening on %s, protecting %v of %s at %d sats or %s PoW difficulty %d (~%.1f s on one CPU core)",
		cfg.Listen, cfg.Protect, cfg.Origin, cfg.PriceSats, p.Algorithm, p.Difficulty, p.ExpectedSeconds())
	if len(cfg.BotPolicies) > 0 {
		log.Printf("bot policies: %v", cfg.BotPolicies)
	}
	log.Fatal(srv.ListenAndServe())
}

// startAdmin serves the admin API and dashboard on their own address
// (localhost by default), separate from the public proxy.
func startAdmin(cfg *config.Config, g *gate.Gate, monitor *admin.Monitor, backend lightning.Backend) {
	opt := admin.Options{Config: cfg, Gate: g, Monitor: monitor}
	if insp, ok := backend.(lightning.Inspector); ok {
		opt.Node = insp
	}
	if cfg.Withdraw.Enabled {
		switch b := backend.(type) {
		case *lightning.Fake:
			opt.Withdrawer = b
		case *lightning.Phoenixd:
			opt.Withdrawer = &lightning.PhoenixdWithdrawer{URL: b.URL, FullPassword: cfg.Withdraw.PhoenixdFullPassword, FeerateSatByte: cfg.Withdraw.FeerateSatByte}
		}
		log.Printf("withdrawals ENABLED to %s (admin dashboard, withdraw password, two confirmations)", cfg.Withdraw.Destination)
	}
	srv := &http.Server{
		Addr:              cfg.Admin.Listen,
		Handler:           admin.New(opt).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	go func() { log.Fatal(srv.ListenAndServe()) }()
	ai := "no AI key"
	if cfg.Admin.AIKey != "" {
		ai = "AI may propose changes"
		if cfg.Admin.AICanApply {
			ai = "AI may apply changes"
		}
	}
	log.Printf("admin dashboard on http://%s (%s)", cfg.Admin.Listen, ai)
}
