package tun

import (
	"errors"
	"game_tun/internal/protocol"
	"log"
	"net"
	"os/exec"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wintun"
)

type Tun struct {
	Name   string
	Ip     string
	Subnet *net.IPNet
	// 发送给服务器的数据队列
	SendGp chan []byte
	// 从服务器接收包队列
	RecvGp  chan *protocol.GamePacket
	adapter *wintun.Adapter
	session wintun.Session
}

func NewTun(name, ip, mask string) *Tun {
	adapter, err := wintun.CreateAdapter(name, "Wintun", nil)
	if err != nil {
		log.Fatal(err)
	}
	sendGp := make(chan []byte, 64)
	return &Tun{
		Ip:      ip,
		Subnet: &net.IPNet{
			IP:   net.ParseIP(ip),
			Mask: net.IPv4Mask(mask[0], mask[1], mask[2], mask[3]),
		}
		Name:    name,
		adapter: adapter,
		SendGp:  sendGp,
	}
}

func (t *Tun) Start() {
	var err error
	defer t.adapter.Close()
	t.session, err = t.adapter.StartSession(0x800000) // 环形缓冲区大小
	if err != nil {
		log.Fatalf("启动会话失败: %v", err)
	}
	defer t.session.End()
	log.Printf("%s 适配器已启动！\n", t.Name)
	exec.Command("netsh", "interface", "ip", "set", "address", t.Name, "static", t.Ip, t.Mask).Run()
	go t.recv()

}

func (t *Tun) recv() {
	waitEvent := t.session.ReadWaitEvent()
	for {
		// 1. 尝试读取数据包
		packet, err := t.session.ReceivePacket()
		switch {
		case err == nil:
			t.SendGp <- packet
			t.session.ReleaseReceivePacket(packet)
		case errors.Is(err, windows.ERROR_NO_MORE_ITEMS):
			windows.WaitForSingleObject(waitEvent, windows.INFINITE)
		default:
			log.Printf("读取发生严重错误: %v", err)
			continue
		}
	}
}

func (t *Tun) handle() {
	for {
		select {
		case packet := <-t.SendGp:
			if len(packet) < 20 || packet[0]>>4 == 6 {
				continue
			}
			dstIp := net.IP(packet[16:20])
			if dstIp.Equal(net.IPv4bcast) || dstIp[3] == 255 { // 255.255.255.255

			}
		}
	}
}
