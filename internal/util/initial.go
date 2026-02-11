//go:build client

package util

import (
	"game_tun/internal/config"
	"log"
)

func InitAll() {
	config.InitConfig()
	err := InitWintunDLL()
	if err != nil {
		log.Fatal(err)
	}
}
