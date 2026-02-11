package util

import (
	"time"

	probing "github.com/prometheus-community/pro-bing"
)

func TestPing(ip string) (time.Duration, float64) {
	pinger, err := probing.NewPinger(ip)
	if err != nil {
		panic(err)
	}

	pinger.SetPrivileged(true)

	pinger.Count = 4
	pinger.Timeout = time.Second * 3

	pinger.OnRecv = func(pkt *probing.Packet) {
	}
	err = pinger.Run()
	if err != nil {
		panic(err)
	}
	stats := pinger.Statistics()
	return stats.AvgRtt, stats.PacketLoss
}
