//go:build client && linux

package tun

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

type Tun struct {
	DeviceName string
	Ip         net.IP
	DnsIp      net.IP
	Subnet     *net.IPNet
	domain     string

	fd      *os.File
	toNet   chan<- []byte
	fromNet <-chan []byte
	bufPool *sync.Pool
}

func NewTun(name, domain string, ipByte []byte, toNet, fromNet chan []byte, bufPool *sync.Pool) Device {
	ip := net.IP(ipByte)
	// 默认 DNS 为网段内第一个地址
	dnsIp := net.IP{ip[0], ip[1], ip[2], 1}
	mask := net.CIDRMask(24, 32)
	subnet := &net.IPNet{
		IP:   ip.Mask(mask),
		Mask: mask,
	}

	// 1. 打开 TUN 设备文件
	fd, err := os.OpenFile("/dev/net/tun", os.O_RDWR, 0)
	if err != nil {
		panic(fmt.Sprintf("无法打开 /dev/net/tun: %v", err))
	}

	// 2. 注册 TUN 设备
	var ifr struct {
		name  [16]byte
		flags uint16
	}
	copy(ifr.name[:], name)
	// IFF_TUN: 创建 TUN 设备 (IP 层)
	// IFF_NO_PI: 不包含额外的包头信息 (Packet Information)
	ifr.flags = unix.IFF_TUN | unix.IFF_NO_PI

	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd.Fd(), uintptr(unix.TUNSETIFF), uintptr(unsafe.Pointer(&ifr)))
	if errno != 0 {
		fd.Close()
		panic(fmt.Sprintf("ioctl(TUNSETIFF) 失败: %v", errno))
	}

	// 3. 配置 IP 地址和 MTU (使用 ip 命令)
	// ip addr add 10.0.x.x/24 dev name
	if output, err := exec.Command("ip", "addr", "add", fmt.Sprintf("%s/24", ip.String()), "dev", name).CombinedOutput(); err != nil {
		fd.Close()
		panic(fmt.Sprintf("配置 IP 失败: %v, Output: %s", err, string(output)))
	}

	// ip link set dev name mtu 1280 up
	if output, err := exec.Command("ip", "link", "set", "dev", name, "mtu", "1280", "up").CombinedOutput(); err != nil {
		fd.Close()
		panic(fmt.Sprintf("启用网卡失败: %v, Output: %s", err, string(output)))
	}

	cleanDomain := domain
	if len(cleanDomain) > 0 && cleanDomain[0] == '.' {
		cleanDomain = cleanDomain[1:]
	}
	// 4. 配置 DNS (针对 systemd-resolved 兼容系统)
	if out, err := exec.Command("resolvectl", "dns", name, dnsIp.String()).CombinedOutput(); err != nil {
		panic(fmt.Sprintf("resolvectl dns 失败: %v, output: %s", err, string(out)))
	}
	if out, err := exec.Command("resolvectl", "domain", name, "~"+cleanDomain).CombinedOutput(); err != nil {
		panic(fmt.Sprintf("resolvectl domain 失败: %v, output: %s", err, string(out)))
	}
	exec.Command("resolvectl", "flush-caches").Run()

	return &Tun{
		Ip:         ip,
		DnsIp:      dnsIp,
		Subnet:     subnet,
		DeviceName: name,
		domain:     domain,
		fd:         fd,
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

func (t *Tun) Dns() net.IP {
	return t.DnsIp
}

func (t *Tun) Close() error {
	// 清理 DNS 配置
	exec.Command("resolvectl", "revert", t.DeviceName).Run()
	return t.fd.Close()
}

func (t *Tun) LUID() uint64 {
	// Linux 下没有 LUID 概念，返回 0 保持接口兼容
	return 0
}

func (t *Tun) Name() string {
	return t.DeviceName
}

func (t *Tun) SendDNS(resp []byte) error {
	_, err := t.fd.Write(resp)
	return err
}

// tunRecv 从 TUN 设备读取数据
func (t *Tun) tunRecv(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
			buf := t.bufPool.Get().([]byte)
			if cap(buf) < 2048 {
				t.bufPool.Put(buf)
				buf = make([]byte, 2048)
			}
			buf = buf[:cap(buf)]

			n, err := t.fd.Read(buf)
			if err != nil {
				if errors.Is(err, os.ErrClosed) {
					t.bufPool.Put(buf[:0])
					return
				}
				log.Printf("TUN 读取错误: %v", err)
				t.bufPool.Put(buf[:0])
				continue
			}

			packet := buf[:n]
			if n < 20 || packet[0]>>4 != 4 {
				t.bufPool.Put(buf[:0])
				continue // 忽略非 IPv4 包
			}

			dstIp := net.IP(packet[16:20])
			// 过滤逻辑：广播、组播、或是发往子网内部的包
			if dstIp.Equal(net.IPv4bcast) || dstIp[3] == 255 || dstIp.IsMulticast() || (t.Subnet.Contains(dstIp) && !dstIp.IsLoopback()) {
				select {
				case t.toNet <- packet:
				case <-ctx.Done():
					t.bufPool.Put(packet[:0])
					return
				default:
					t.bufPool.Put(packet[:0])
					log.Println("toNet 队列已满")
				}
			} else {
				t.bufPool.Put(packet[:0])
			}
		}
	}
}

// tunSend 将数据包写入 TUN 设备
func (t *Tun) tunSend(ctx context.Context, headerLength int) {
	for {
		select {
		case payload := <-t.fromNet:
			if len(payload) > headerLength {
				_, err := t.fd.Write(payload[headerLength:])
				if err != nil {
					log.Printf("TUN 写入错误: %v", err)
				}
			}
			t.bufPool.Put(payload[:0])
		case <-ctx.Done():
			return
		}
	}
}
