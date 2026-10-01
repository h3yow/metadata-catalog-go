// Command catalogd runs the metadata catalog as a local HTTP daemon.
//
//	go run ./cmd/catalogd -addr 127.0.0.1:8080
package main

import (
	"flag"
	"log"
	"net/http"

	"github.com/h3yow/metadata-catalog-go/catalog"
	"github.com/h3yow/metadata-catalog-go/server"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "listen address")
	flag.Parse()

	c := catalog.New()
	log.Printf("catalogd listening on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, server.New(c)))
}
