// Command demo-origin is a stand-in website for trying Spikes402 locally.
//
//	demo-origin -listen :8080
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
)

func main() {
	listen := flag.String("listen", ":8080", "address to listen on")
	flag.Parse()

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "Welcome! This page is free for everyone.")
	})
	http.HandleFunc("/premium/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "Premium content at %s. Thank you for paying (or working)!\n", r.URL.Path)
	})
	log.Printf("demo origin listening on %s", *listen)
	log.Fatal(http.ListenAndServe(*listen, nil))
}
