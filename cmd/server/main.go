package main

import (
	"context"
	"game_tun/internal/config"
	"game_tun/internal/transport"
)

func main() {
	config.InitConfig()
	hub := transport.NewHub()
	hub.Run(context.Background())
}
