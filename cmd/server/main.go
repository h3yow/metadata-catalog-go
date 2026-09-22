// Command server 启动本地元数据目录 HTTP 服务。
package main

import (
	"flag"
	"log"
	"net/http"

	"github.com/h3yow/metadata-catalog-go/internal/catalog"
	"github.com/h3yow/metadata-catalog-go/internal/server"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "listen address")
	flag.Parse()

	srv := server.New(catalog.New())
	log.Printf("metadata catalog listening on %s", *addr)
	if err := http.ListenAndServe(*addr, srv.Handler()); err != nil {
		log.Fatal(err)
	}
}
