// Command server runs the metadata catalog as a long-lived local
// HTTP service, so it can be used with curl or any HTTP client
// without writing Go code.
//
//	go run ./cmd/server [-addr 127.0.0.1:8080]
//
// Endpoints (all JSON):
//
//	POST /api/entities                               register a report
//	GET  /api/entities?tag=&owner=&keyword=          search
//	GET  /api/entities/{kind}/{name}                 current merged state
//	GET  /api/entities/{kind}/{name}/history         version history
//	GET  /api/entities/{kind}/{name}/sources         per-source contributions
//	GET  /api/entities/{kind}/{name}/downstream      impact analysis
//	GET  /api/entities/{kind}/{name}/upstream        reverse impact analysis
//	POST /api/lineage                                register a lineage edge
package main

import (
	"flag"
	"log"
	"net/http"

	"github.com/h3yow/metadata-catalog-go/catalog"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "listen address")
	flag.Parse()

	c := catalog.New()
	log.Printf("metadata catalog listening on http://%s", *addr)
	log.Fatal(http.ListenAndServe(*addr, catalog.NewHTTPHandler(c)))
}
