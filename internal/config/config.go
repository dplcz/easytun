package config

import (
	"time"
)

var SeverIp string
var SeverPort int
var DeviceName string
var ReadTimeout time.Duration
var PingTime time.Duration

func init() {
	SeverIp = "127.0.0.1"
	SeverPort = 8080
	DeviceName = "default"
	ReadTimeout = 5
	PingTime = 1
}
