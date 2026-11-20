// Command spikes-bot is a tiny example bot: it fetches a URL and, when a
// Spikes402 gate answers 402, solves the proof-of-work or pays.
//
//	spikes-bot -mode pow http://localhost:8402/premium/article
//	spikes-bot -mode pay http://localhost:8402/premium/article
//
// Paying only works against a proxy running the fake backend for now.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"time"

	"github.com/Choffolo/spikes402/client"
)

func main() {
	mode := flag.String("mode", "pow", `"pow" to do the work, "pay" to pay (fake backend only)`)
	maxDifficulty := flag.Int("max-difficulty", 28, "refuse proof-of-work puzzles harder than this")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: spikes-bot [-mode pow|pay] URL")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	target := flag.Arg(0)
	u, err := url.Parse(target)
	if err != nil {
		log.Fatal(err)
	}

	log.SetFlags(0)
	log.SetPrefix("bot: ")
	c := &client.Client{
		Mode:          client.Mode(*mode),
		Payer:         client.FakePayer{ProxyURL: u.Scheme + "://" + u.Host},
		MaxDifficulty: *maxDifficulty,
		Logf:          log.Printf,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	start := time.Now()
	log.Printf("GET %s", target)
	resp, err := c.Get(ctx, target)
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	log.Printf("%s in %s", resp.Status, time.Since(start).Round(time.Millisecond))
	os.Stdout.Write(body)
	if resp.StatusCode != 200 {
		os.Exit(1)
	}
}
