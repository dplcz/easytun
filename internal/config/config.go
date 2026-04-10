package config

import (
	"easytun/assets"
	"log"
	"os"
	"time"

	"github.com/pelletier/go-toml/v2"
)

var ServerIp string
var ServerPort int
var DeviceName string
var ClientID [16]byte
var ReadTimeout time.Duration
var RetentionTime time.Duration
var PingTime time.Duration
var SendWorkers int
var RecvWorkers int
var CheckPort int
var EnableP2P bool
var EnableUi bool
var EnableCompress bool

type Configuration struct {
	Server      ServerConfig      `toml:"server"`
	Device      DeviceConfig      `toml:"device"`
	Performance PerformanceConfig `toml:"performance"`
	Features    FeaturesConfig    `toml:"features"`
}

type ServerConfig struct {
	ServerIp      string `toml:"server_ip"`
	ServerPort    int    `toml:"server_port"`
	CheckPort     int    `toml:"check_port"`
	RetentionTime string `toml:"retention_time"`
}

type DeviceConfig struct {
	DeviceName string `toml:"device_name"`
}

type PerformanceConfig struct {
	ReadTimeout string `toml:"read_timeout"`
	PingTime    string `toml:"ping_time"`
	SendWorkers int    `toml:"send_workers"`
	RecvWorkers int    `toml:"recv_workers"`
}

type FeaturesConfig struct {
	EnableP2P      bool `toml:"enable_p2p"`
	EnableUi       bool `toml:"enable_ui"`
	EnableCompress bool `toml:"enable_compress"`
}

func InitConfig(localPath string) {
	conf := Configuration{}
	var data []byte
	var err error

	// 2. 决定使用哪个数据源
	if localPath != "" {
		// 使用用户指定的配置文件
		//log.Println("loading config file:", localPath)
		data, err = os.ReadFile(localPath)
	} else if _, err = os.Stat("config.toml"); err == nil {
		// 使用本地已存在的 config.toml
		//log.Println("loading local config file: config.toml")
		data, err = os.ReadFile("config.toml")
	} else {
		// 使用内置的兜底默认配置
		//log.Println("loading embedded default config")
		data = assets.ConfigBytes
		err = nil
	}
	if err != nil {
		panic(err)
	}
	if err = toml.Unmarshal(data, &conf); err != nil {
		panic(err)
	}

	ServerIp = conf.Server.ServerIp
	ServerPort = conf.Server.ServerPort
	CheckPort = conf.Server.CheckPort
	RetentionTime, err = time.ParseDuration(conf.Server.RetentionTime)
	if err != nil {
		//log.Printf("failed to parse retention_time: %v, use default 5m\n", err)
		RetentionTime = 5 * time.Minute
	}
	DeviceName = conf.Device.DeviceName
	if len(DeviceName) > 10 || len(DeviceName) < 1 {
		panic("Device name must be between 1 and 10")
	}

	// 解析 Duration 字符串
	ReadTimeout, err = time.ParseDuration(conf.Performance.ReadTimeout)
	if err != nil {
		log.Printf("failed to parse read_timeout: %v, use default 10s\n", err)
		ReadTimeout = 10 * time.Second
	}

	PingTime, err = time.ParseDuration(conf.Performance.PingTime)
	if err != nil {
		log.Printf("failed to parse ping_time: %v, use default 1s\n", err)
		PingTime = 1 * time.Second
	}

	SendWorkers = conf.Performance.SendWorkers
	if SendWorkers < 1 {
		SendWorkers = 1
	}
	RecvWorkers = conf.Performance.RecvWorkers
	if RecvWorkers < 1 {
		RecvWorkers = 1
	}
	EnableP2P = conf.Features.EnableP2P
	EnableUi = conf.Features.EnableUi
	EnableCompress = conf.Features.EnableCompress

	// 持久化 ClientID 逻辑
	cidPath := ".client_id"
	if fileData, err := os.ReadFile(cidPath); err == nil && len(fileData) == 16 {
		copy(ClientID[:], fileData)
	} else {
		// 生成新的随机 ClientID
		for i := 0; i < 16; i++ {
			ClientID[i] = uint8(time.Now().UnixNano() % 256)
		}
		_ = os.WriteFile(cidPath, ClientID[:], 0644)
	}
}
