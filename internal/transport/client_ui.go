//go:build client

package transport

import (
	"context"
	"easytun/internal/config"
	"net"
	"strconv"
	"time"

	"github.com/pterm/pterm"
	"golang.org/x/net/ipv4"
)

// initUi 在终端界面显示客户端的基础运行信息
func (t *ClientTransport) initUi() {
	//t.board.AddInfo("Name", config.DeviceName, nil)
	t.board.AddInfo("Receiver Count", strconv.Itoa(config.RecvWorkers), pterm.LightCyan)
	t.board.AddInfo("Sender Count", strconv.Itoa(config.SendWorkers), pterm.LightCyan)
	if config.EnableP2P {
		t.board.AddInfo("P2P status", "Enable", pterm.LightGreen)
	} else {
		t.board.AddInfo("P2P status", "Disable", pterm.LightRed)
	}
	if config.EnableCompress {
		t.board.AddInfo("Compress", "Enable", pterm.LightGreen)
	} else {
		t.board.AddInfo("Compress", "Disable", pterm.LightRed)
	}
}

// testBroadCast 定时构造并发送广播 IP 包，用于压力测试或连通性检测
func (t *ClientTransport) testBroadCast(ctx context.Context, second time.Duration) error {
	timer := time.NewTicker(time.Millisecond * second)
	broadCastHeader := &ipv4.Header{
		Version:  ipv4.Version,
		Len:      ipv4.HeaderLen,
		TOS:      0x0,
		TotalLen: ipv4.HeaderLen,
		TTL:      64,
		Protocol: 17,            // UDP
		Dst:      net.IPv4bcast, // 255.255.255.255
		Src:      t.localIp,     // 你的虚拟 IP
	}
	bch, err := broadCastHeader.Marshal()
	if err != nil {
		return err
	}
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			//log.Println("执行广播...")
			t.FromTun <- bch
		case <-ctx.Done():
			return nil
		}
	}
}
