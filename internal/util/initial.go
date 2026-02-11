//go:build client

package util

import (
	"game_tun/internal/config"
	"game_tun/internal/tun"
	"log"
)

func InitAll() {
	config.InitConfig()
	err := tun.InitWintunDLL()
	if err != nil {
		log.Fatal(err)
	}
}
