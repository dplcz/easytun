//go:build client

package tun

import (
	"context"
	"errors"
	"fmt"
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
	bufPool *sync.Pool
}

func NewTun(name string, ipByte []byte, toNet, fromNet chan []byte, bufPool *sync.Pool) Device {
	ip := net.IP(ipByte)
	mask := net.CIDRMask(24, 32)
	subnet := &net.IPNet{
		IP:   ip.Mask(mask),
		Mask: mask,
	}

	adapter, err := wintun.CreateAdapter(name, "Wintun", nil)
	if err != nil {
		panic(fmt.Sprintf("Failed to create Wintun adapter: %v", err))
	}
	session, err := adapter.StartSession(0x800000) // 环形缓冲区大小
	if err != nil {
		panic(fmt.Sprintf("启动会话失败: %v", err))
	}
	//log.Printf("%s 适配器已启动!\n", name)
	cmd := exec.Command("netsh", "interface", "ip", "set", "address", name, "static", ip.String(), net.IP(subnet.Mask).String())
	if output, err := cmd.CombinedOutput(); err != nil {
		panic(fmt.Sprintf("配置 IP 失败: %v, Output: %s", err, string(output)))
	}
	//log.Printf("配置ip成功! 当前虚拟ip为: %s \n", ip.String())
	cmd = exec.Command("netsh", "interface", "ipv4", "set", "subinterface", name, "mtu=1280", "store=persistent")
	if output, err := cmd.CombinedOutput(); err != nil {
		panic(fmt.Sprintf("配置 IP 失败: %v, Output: %s", err, string(output)))
	}
	//log.Printf("配置mtu成功! \n")
	cmdStr := fmt.Sprintf("[Console]::OutputEncoding = [System.Text.Encoding]::UTF8; Set-NetConnectionProfile -InterfaceAlias '%s' -NetworkCategory Private", name)
	cmd = exec.Command("powershell", "-Command", cmdStr)
	if output, err := cmd.CombinedOutput(); err != nil {
		panic(fmt.Sprintf("配置 IP 失败: %v, Output: %s", err, string(output)))
	}
	//log.Println("配置专用网络成功!")

	return &Tun{
		Ip:         ip,
		Subnet:     subnet,
		DeviceName: name,
		adapter:    adapter,
		session:    session,
		toNet:      toNet,
		fromNet:    fromNet,
		bufPool:    bufPool,
	}
}

func (t *Tun) Start(ctx context.Context, headerLength int) {
	wg := sync.WaitGroup{}
	wg.Add(2)
	go func() {
		defer wg.Done()
		t.tunSend(ctx, headerLength)
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

// tunRecv 从tun接收数据
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
					packetLen := len(packet)
					if packetLen < 20 || packet[0]>>4 != 4 {
						return // 忽略非法包
					}
					dstIp := net.IP(packet[16:20])

					if dstIp.Equal(net.IPv4bcast) || dstIp[3] == 255 || dstIp.IsMulticast() || (t.Subnet.Contains(dstIp) && !dstIp.IsLoopback()) {
						buf := t.bufPool.Get().([]byte)
						if cap(buf) < packetLen {
							t.bufPool.Put(buf)
							buf = make([]byte, packetLen)
						}
						buf = buf[:packetLen]
						copy(buf, packet)
						select {
						case t.toNet <- buf:
						case <-ctx.Done():
							t.bufPool.Put(buf[:0])
							return
						default:
							t.bufPool.Put(buf[:0])
							log.Println("toNet 已满")
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
func (t *Tun) tunSend(ctx context.Context, headerLength int) {
	batchSize := 64
	payloadBatch := make([][]byte, 0, batchSize)
	for {
		select {
		case payload := <-t.fromNet:
			if len(payload) > headerLength {
				payloadBatch = append(payloadBatch, payload)
			DrainLoop:
				for len(payloadBatch) < batchSize {
					select {
					case extraPacket := <-t.fromNet:
						if len(payload) > headerLength {
							payloadBatch = append(payloadBatch, extraPacket)
						}
					default:
						break DrainLoop
					}
				}
			}
			for _, p := range payloadBatch {
				packetBuffer, err := t.session.AllocateSendPacket(len(p) - headerLength)
				if err == nil {
					copy(packetBuffer, p[headerLength:])
					t.session.SendPacket(packetBuffer)
					t.bufPool.Put(p[:0])
				} else {
					log.Printf("Wintun Allocate 失败: %v", err)
					t.bufPool.Put(p[:0])
				}
			}
			payloadBatch = payloadBatch[:0]
		case <-ctx.Done():
			return
		}
	}
}
