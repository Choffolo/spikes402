// Command spikes402-mcp is an MCP server (stdio) that lets an AI assistant
// watch a Spikes402 proxy and propose settings changes through its admin
// API. It uses the AI key (admin.ai_key), which can read and propose,
// apply only if the owner allowed it, and never withdraw.
//
//	SPIKES402_AI_KEY=... SPIKES402_ADMIN_URL=http://127.0.0.1:8404 spikes402-mcp
package main

import (
	"context"
	"log"
	"os"

	"github.com/Choffolo/spikes402/internal/mcp"
)

func main() {
	log.SetOutput(os.Stderr) // stdout is the MCP channel
	key := os.Getenv("SPIKES402_AI_KEY")
	if key == "" {
		log.Fatal("set SPIKES402_AI_KEY to the admin.ai_key of your Spikes402 proxy")
	}
	url := os.Getenv("SPIKES402_ADMIN_URL")
	if url == "" {
		url = "http://127.0.0.1:8404"
	}
	s := &mcp.Server{Name: "spikes402", Version: "0.1", Tools: mcp.AdminTools(&mcp.AdminClient{BaseURL: url, Key: key})}
	if err := s.Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
		log.Fatal(err)
	}
}
