package tun

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"game_tun/internal/config"
	"game_tun/internal/protocol"
	"io"
	"log"
	"net"
	"os/exec"
	"sync"
	"time"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wintun"
)

type Tun struct {
	Name   string
	Ip     net.IP
	Subnet *net.IPNet
	// 发送给服务器的数据队列
	ControlRecvChan chan *protocol.GamePacket
	ControlSendChan chan *protocol.GamePacket
	// 从服务器接收包队列
	PacketRecvChan chan []byte
	PacketSendChan chan []byte
	adapter        *wintun.Adapter
	session        wintun.Session
	controlConn    net.Conn
	dataConn       *net.UDPConn
	wg             sync.WaitGroup
}

func NewTun(name, ipStr string) *Tun {
	adapter, err := wintun.CreateAdapter(name, "Wintun", nil)
	if err != nil {
		log.Fatal(err)
	}
	ip := net.ParseIP(ipStr)
	mask := net.CIDRMask(24, 32)

	return &Tun{
		Ip: ip,
		Subnet: &net.IPNet{
			IP:   ip.Mask(mask),
			Mask: mask,
		},
		Name:            name,
		adapter:         adapter,
		ControlRecvChan: make(chan *protocol.GamePacket, 32),
		ControlSendChan: make(chan *protocol.GamePacket, 32),
		PacketRecvChan:  make(chan []byte, 64),
		PacketSendChan:  make(chan []byte, 64),
	}
}

func (t *Tun) Start(ctx context.Context) {
	var err error
	defer t.adapter.Close()
	t.session, err = t.adapter.StartSession(0x800000) // 环形缓冲区大小
	if err != nil {
		log.Fatalf("启动会话失败: %v", err)
	}
	defer t.session.End()
	log.Printf("%s 适配器已启动！\n", t.Name)
	cmd := exec.Command("netsh", "interface", "ip", "set", "address", t.Name, "static", t.Ip.String(), net.IP(t.Subnet.Mask).String())
	if output, err := cmd.CombinedOutput(); err != nil {
		log.Fatalf("配置 IP 失败: %v, Output: %s", err, string(output))
	}
	t.wg.Add(2)
	go func() {
		defer t.wg.Done()
		t.handle(ctx)
	}()
	go func() {
		defer t.wg.Done()
		t.recv(ctx)
	}()

	<-ctx.Done()
	t.wg.Wait()
}

func (t *Tun) Stop(cancel context.CancelFunc) {
	t.session.End()
	cancel()
}

func (t *Tun) connectServer(ctx context.Context) error {
	luid := make([]byte, 8)
	binary.BigEndian.PutUint64(luid, t.adapter.LUID())
	handshakePacket := protocol.NewGamePacket([4]byte(t.Ip), [4]byte{}, protocol.TypeHandshake, luid)
	tempConnTcp, err := net.Dial("tcp", fmt.Sprintf("%s:%d", config.SeverIp, config.SeverPort))
	if err != nil {
		return err
	}
	t.controlConn = tempConnTcp
	t.controlConn.Write(handshakePacket.Encode())
	// TODO 处理握手响应
	go t.controlRecv(ctx)
	go t.heartbeat(ctx)
	return nil

}

func (t *Tun) heartbeat(ctx context.Context) {
	timer := time.NewTimer(time.Second * 5)
	defer timer.Stop()
	hearbeatPacket := protocol.NewGamePacket([4]byte(t.Ip), [4]byte{}, protocol.TypeKeepAlive, nil)
	for {
		select {
		case <-timer.C:
			t.ControlSendChan <- hearbeatPacket
		case <-ctx.Done():
			return
		}
	}
}

func (t *Tun) controlRecv(ctx context.Context) {
	const HeadSize = 13
	const ReadTimeout = 30 * time.Second
	headerBuf := make([]byte, 13)
	for ctx.Err() == nil {
		gp := &protocol.GamePacket{}
		t.controlConn.SetReadDeadline(time.Now().Add(ReadTimeout))
		_, err := io.ReadFull(t.controlConn, headerBuf)
		if err != nil {
			log.Println(err)
			return
		}

		err = gp.ParseHead(headerBuf)
		if err != nil {
			log.Println(err)
			return
		}

		payload := make([]byte, gp.Length)
		_, err = io.ReadFull(t.controlConn, payload)
		if err != nil {
			log.Println(err)
			return
		}

		select {
		case t.ControlRecvChan <- gp:
			continue
		case <-ctx.Done():
			return
		}

	}
}

func (t *Tun) recv(ctx context.Context) {
	waitEvent := t.session.ReadWaitEvent()
	for {
		select {
		case <-ctx.Done():
			return
		default:
			packet, err := t.session.ReceivePacket()
			switch {
			case err == nil:
				pktCopy := make([]byte, len(packet))
				copy(pktCopy, packet)
				t.session.ReleaseReceivePacket(packet)
				select {
				case t.PacketSendChan <- pktCopy:
					// 发送成功
				case <-ctx.Done():
					return // 发送时被取消，直接退出
				}
			case errors.Is(err, windows.ERROR_NO_MORE_ITEMS):
				_, err := windows.WaitForSingleObject(waitEvent, windows.INFINITE)
				if err != nil {
					log.Println(err)
					return
				}
			default:
				log.Printf("读取发生严重错误: %v", err)
				continue
			}
		}
	}
}

func (t *Tun) handle(ctx context.Context) {
	for {
		select {
		case packet := <-t.PacketSendChan:
			if len(packet) < 20 || packet[0]>>4 == 6 {
				continue
			}
			dstIp := net.IP(packet[16:20])
			if dstIp.Equal(net.IPv4bcast) || dstIp[3] == 255 || dstIp.IsMulticast() || t.Subnet.Contains(dstIp) || !dstIp.IsLoopback() {
				gp := protocol.NewGamePacket([4]byte(t.Ip), [4]byte(packet[16:20]), protocol.TypeData, packet)

				log.Printf("recv packet to %s", dstIp.String())
			}
		case <-ctx.Done():
			return
		}
	}
}
