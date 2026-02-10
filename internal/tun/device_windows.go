package tun

import (
	"context"
	"errors"
	"log"
	"net"
	"os/exec"
	"sync"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wintun"
)

type Tun struct {
	DeviceName string
	Ip         net.IP
	Subnet     *net.IPNet
	// tun收发队列
	toNet   chan<- []byte
	fromNet <-chan []byte
	adapter *wintun.Adapter
	session wintun.Session
}

func NewTun(name string, ipByte []byte, toNet, fromNet chan []byte) Device {
	ip := net.IP(ipByte)
	mask := net.CIDRMask(24, 32)
	subnet := &net.IPNet{
		IP:   ip.Mask(mask),
		Mask: mask,
	}

	adapter, err := wintun.CreateAdapter(name, "Wintun", nil)
	if err != nil {
		log.Fatal(err)
	}
	session, err := adapter.StartSession(0x800000) // 环形缓冲区大小
	if err != nil {
		log.Fatalf("启动会话失败: %v", err)
	}
	log.Printf("%s 适配器已启动！\n", name)
	cmd := exec.Command("netsh", "interface", "ip", "set", "address", name, "static", ip.String(), net.IP(subnet.Mask).String())
	if output, err := cmd.CombinedOutput(); err != nil {
		log.Fatalf("配置 IP 失败: %v, Output: %s", err, string(output))
	}

	return &Tun{
		Ip:         ip,
		Subnet:     subnet,
		DeviceName: name,
		adapter:    adapter,
		session:    session,
		toNet:      toNet,
		fromNet:    fromNet,
	}
}

func (t *Tun) Start(ctx context.Context) {
	wg := sync.WaitGroup{}
	wg.Add(2)
	go func() {
		defer wg.Done()
		t.tunSend(ctx)
	}()
	go func() {
		defer wg.Done()
		t.tunRecv(ctx)
	}()
	select {
	case <-ctx.Done():
		t.Close()
	}
	wg.Wait()
}

// Stop
func (t *Tun) Close() error {
	t.session.End()
	err := t.adapter.Close()
	return err
}

func (t *Tun) LUID() uint64 {
	return t.adapter.LUID()
}
func (t *Tun) Name() string {
	return t.DeviceName
}

// tunRecv 从tun接收数据并封包
func (t *Tun) tunRecv(ctx context.Context) {
	waitEvent := t.session.ReadWaitEvent()
	for {
		select {
		case <-ctx.Done():
			return
		default:
			packet, err := t.session.ReceivePacket()
			switch {
			case err == nil:
				func() {
					// 释放 packet
					defer t.session.ReleaseReceivePacket(packet)

					if len(packet) < 20 || packet[0]>>4 != 4 {
						return // 忽略非法包
					}
					dstIp := net.IP(packet[16:20])

					if dstIp.Equal(net.IPv4bcast) || dstIp[3] == 255 || dstIp.IsMulticast() || (t.Subnet.Contains(dstIp) && !dstIp.IsLoopback()) {
						// TODO 使用对象池
						payloadCopy := make([]byte, len(packet))
						copy(payloadCopy, packet)
						select {
						case t.toNet <- payloadCopy:
						case <-ctx.Done():
							return
						}
					}

				}() // 执行闭包
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

// tunSend 处理接收的包并转发给tun
func (t *Tun) tunSend(ctx context.Context) {
	for {
		select {
		case payload := <-t.fromNet:
			if len(payload) > 0 {
				packetBuffer, err := t.session.AllocateSendPacket(len(payload))
				if err == nil {
					copy(packetBuffer, payload)
					t.session.SendPacket(packetBuffer)
				} else {
					log.Printf("Wintun Allocate 失败: %v", err)
				}
			}

		case <-ctx.Done():
			return
		}
	}
}
