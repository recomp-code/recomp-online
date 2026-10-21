// recomp-online: lobby, rooms, invitations and the match relay for the PS2 recompilation ports
// (DBZ Budokai Tenkaichi 4, Bloody Roar 4, ...). One binary, standard library only.
//
//	recomp-online --http 127.0.0.1:8090 --relay :7777 --relay-public tu.dominio:7777 --data /var/lib/recomp-online
//
// The HTTP API sits behind Caddy (HTTPS); the relay is plain UDP (the match tokens are random).
package main

import (
	"flag"
	"log"
	"net/http"
	"time"
)

func main() {
	httpAddr := flag.String("http", "127.0.0.1:8090", "dirección del API (Caddy hace de HTTPS por delante)")
	relayAddr := flag.String("relay", ":7777", "puerto UDP del relay de las partidas")
	relayPublic := flag.String("relay-public", "", "host:puerto del relay que se da a los jugadores (tu dominio)")
	dataDir := flag.String("data", "./data", "carpeta de cuentas y sesiones")
	flag.Parse()
	if *relayPublic == "" {
		log.Fatal("falta --relay-public (por ejemplo tu.dominio:7777)")
	}
	st, err := openStore(*dataDir)
	if err != nil {
		log.Fatalf("datos: %v", err)
	}
	rl, err := newRelay(*relayAddr)
	if err != nil {
		log.Fatalf("relay: %v", err)
	}
	h := newHub(st, rl, *relayPublic)
	if h.stats, err = openStats(*dataDir); err != nil {
		log.Fatalf("estadísticas: %v", err)
	}
	a := &api{st: st, hub: h, limiter: newLimiter(), conns: newConnLimit()}
	srv := &http.Server{Addr: *httpAddr, Handler: a.routes(), ReadHeaderTimeout: 10 * time.Second}
	log.Printf("recomp-online: API %s, relay UDP %s (público %s), datos %s", *httpAddr, *relayAddr, *relayPublic, *dataDir)
	log.Fatal(srv.ListenAndServe())
}
