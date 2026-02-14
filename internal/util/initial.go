//go:build client

package util

import (
	"easytun/internal/config"
	"easytun/internal/tun"
	"log"
)

func InitAll() {
	config.InitConfig()
	err := tun.InitWintunDLL()
	if err != nil {
		log.Fatal(err)
	}
}
