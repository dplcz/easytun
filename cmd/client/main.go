package main

import (
	"game_tun/internal/transport"
	"game_tun/internal/util"
)

func main() {
	util.InitAll()
	tp := transport.NewTransport()

	tp.ListenAndServe()
}
