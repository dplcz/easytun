package main

import (
	"context"
	"game_tun/internal/config"
	"game_tun/internal/transport"
	"log"
	"net/http"
	_ "net/http/pprof"
)

func main() {
	go func() {
		log.Println(http.ListenAndServe(":10021", nil))
	}()
	config.InitConfig()
	hub := transport.NewHub()
	hub.Run(context.Background())
}
