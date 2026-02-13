package config

import (
	"bytes"
	"encoding/json"
	"game_tun/assets"
	"log"
	"time"
)

var ServerIp string
var ServerPort int
var DeviceName string
var ReadTimeout time.Duration
var PingTime time.Duration
var SendWorkers int
var RecvWorkers int

type Configuration struct {
	ServerIp    string        `json:"server_ip"`
	ServerPort  int           `json:"server_port"`
	DeviceName  string        `json:"device_name"`
	ReadTimeout time.Duration `json:"read_timeout"`
	PingTime    time.Duration `json:"ping_time"`
	SendWorkers int           `json:"send_workers"`
	RecvWorkers int           `json:"recv_workers"`
}

func InitConfig() {
	conf := Configuration{}
	confBuf := bytes.NewReader(assets.ConfigBytes)
	err := json.NewDecoder(confBuf).Decode(&conf)
	if err != nil {
		log.Fatal()
	}
	ServerIp = conf.ServerIp
	ServerPort = conf.ServerPort
	DeviceName = conf.DeviceName
	ReadTimeout = conf.ReadTimeout
	PingTime = conf.PingTime
	SendWorkers = conf.SendWorkers
	if conf.SendWorkers < 1 {
		conf.SendWorkers = 1
	}
	RecvWorkers = conf.RecvWorkers
	if conf.RecvWorkers < 1 {
		conf.RecvWorkers = 1
	}

	log.Println("初始化配置成功!")
}
