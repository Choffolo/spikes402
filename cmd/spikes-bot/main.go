// Command spikes-bot is a tiny example bot: it fetches a URL and, when a
// Spikes402 gate answers 402, solves the proof-of-work (sha256d or
// argon2id) or pays.
//
//	spikes-bot -mode pow http://localhost:8402/premium/article
//	spikes-bot -mode pay http://localhost:8402/premium/article
//
// With -sign it also signs its requests with Web Bot Auth (an Ed25519 key,
// generated or loaded from -key-file) so the site can recognise it:
//
//	spikes-bot -sign -serve-directory 127.0.0.1:8403 http://localhost:8402/premium/article
//
// Paying only works against a proxy running the fake backend for now.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/Choffolo/spikes402/client"
	"github.com/Choffolo/spikes402/webbotauth"
)

func main() {
	mode := flag.String("mode", "pow", `"pow" to do the work, "pay" to pay (fake backend only)`)
	maxDifficulty := flag.Int("max-difficulty", 28, "refuse sha256d puzzles harder than this")
	maxArgon := flag.Int("max-argon-difficulty", 14, "refuse argon2id puzzles harder than this")
	maxArgonMem := flag.Int("max-argon-memory", 64, "refuse argon2id puzzles needing more MiB than this")
	userAgent := flag.String("user-agent", "spikes-bot/0.1 (+https://github.com/Choffolo/spikes402)", "User-Agent header")
	doSign := flag.Bool("sign", false, "sign requests with Web Bot Auth (HTTP Message Signatures, Ed25519)")
	keyFile := flag.String("key-file", "", "Ed25519 key file for -sign; created if missing (default: a new key each run)")
	agentURL := flag.String("agent-url", "", "https URL where your key directory is published (Signature-Agent)")
	serveDir := flag.String("serve-directory", "", `serve the key directory on this address, e.g. "127.0.0.1:8403" (testing)`)
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: spikes-bot [flags] URL")
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
		Mode:               client.Mode(*mode),
		Payer:              client.FakePayer{ProxyURL: u.Scheme + "://" + u.Host},
		MaxDifficulty:      *maxDifficulty,
		MaxArgonDifficulty: *maxArgon,
		MaxArgonMemoryMiB:  *maxArgonMem,
		UserAgent:          *userAgent,
		Logf:               log.Printf,
	}
	if *doSign {
		if c.Signer, err = signer(*keyFile, *agentURL, *serveDir); err != nil {
			log.Fatal(err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
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
	if class := resp.Header.Get("Spikes402-Class"); class != "" {
		log.Printf("the site classified us as: %s", class)
	}
	os.Stdout.Write(body)
	if resp.StatusCode != 200 {
		os.Exit(1)
	}
}

// signer loads or creates the key, prints the key directory and, with
// -serve-directory, publishes it.
func signer(keyFile, agentURL, serveAddr string) (*webbotauth.Signer, error) {
	key, err := loadOrCreateKey(keyFile)
	if err != nil {
		return nil, err
	}
	if serveAddr != "" {
		ln, err := net.Listen("tcp", serveAddr)
		if err != nil {
			return nil, err
		}
		mux := http.NewServeMux()
		mux.Handle(webbotauth.DirectoryPath, webbotauth.DirectoryHandler(key))
		go http.Serve(ln, mux)
		if agentURL == "" {
			agentURL = "http://" + ln.Addr().String() + webbotauth.DirectoryPath
		}
		log.Printf("serving key directory at %s", agentURL)
	}
	if agentURL == "" {
		return nil, errors.New("-sign needs -agent-url (where your key directory is published) or -serve-directory")
	}
	if !strings.HasPrefix(agentURL, "https://") {
		log.Printf("note: %s is not https; only a gate in web_bot_auth_dev_mode will accept it", agentURL)
	}
	log.Printf("signing requests with key %s; publish this at %s:\n%s",
		webbotauth.Thumbprint(key.Public().(ed25519.PublicKey)), agentURL, webbotauth.DirectoryJSON(key.Public().(ed25519.PublicKey)))
	return &webbotauth.Signer{Key: key, AgentURL: agentURL}, nil
}

func loadOrCreateKey(file string) (ed25519.PrivateKey, error) {
	if file != "" {
		data, err := os.ReadFile(file)
		if err == nil {
			return webbotauth.ParseKey(strings.TrimSpace(string(data)))
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	if file != "" {
		if err := os.WriteFile(file, []byte(webbotauth.FormatKey(key)+"\n"), 0o600); err != nil {
			return nil, err
		}
		log.Printf("new key saved in %s (keep it private)", file)
	}
	return key, nil
}
