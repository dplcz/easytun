//go:build client

package transport

import (
	"bufio"
	"context"
	"easytun/internal/config"
	"easytun/internal/protocol"
	"easytun/internal/stun"
	"easytun/internal/tun"
	"easytun/internal/ui"
	"easytun/internal/util"
	"fmt"
	"log"
	"net"
	"os"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/sync/errgroup"
)

// ClientTransport 负责客户端的网络传输层逻辑，包括 TUN 设备交互、UDP 数据转发及 P2P 打洞
type ClientTransport struct {
	natType         uint8   // 本地 NAT 类型
	sendPacketCount *uint64 // 已发送数据包计数
	recvPacketCount *uint64 // 已接收数据包计数
	status          *uint32 // 程序运行状态

	device tun.Device // TUN 设备接口

	FromTun chan []byte // 从 TUN 设备接收的数据通道
	FromNet chan []byte // 写入 TUN 设备的数据通道

	// 服务器收发队列
	ControlRecvChan chan *protocol.GamePacket // 控制消息接收队列
	ControlSendChan chan *protocol.GamePacket // 控制消息发送队列

	controlConn *websocket.Conn // WebSocket 控制连接封装
	dataConn    *net.UDPConn    // UDP 数据连接
	serverAddr  *net.UDPAddr    // 服务端 UDP 地址
	checkAddr   *net.UDPAddr    // P2P Check 服务地址

	p2pRouter atomic.Value // P2P 路由表 (map[[4]byte]*stun.P2PStatus)
	routerMtx sync.Mutex   // 路由表修改锁

	localIp net.IP     // 本地虚拟 IP
	bufPool *sync.Pool // 字节缓冲区对象池

	board *ui.Board // UI 显示面板

	noiseMgr *protocol.NoiseManager // Noise 协议管理器
}

// NewTransport 创建并初始化客户端传输层实例（仅初始化基础资源，不建立连接）
func NewTransport() *ClientTransport {
	defer func() {
		if err := recover(); err != nil {
			fmt.Fprintf(os.Stderr, "\n--- Panic ---\n")
			fmt.Fprintf(os.Stderr, "错误详情: %v\n\n", err)
			debug.PrintStack()

			fmt.Println("程序已崩溃。按 [回车键] 退出...")
			bufio.NewReader(os.Stdin).ReadString('\n')
			os.Exit(1)
		}
	}()

	sendPacketCount := new(uint64)
	recvPacketCount := new(uint64)
	status := new(uint32)
	outerChan := make(chan []byte, 256)
	innerChan := make(chan []byte, 64)
	controlRecvChan := make(chan *protocol.GamePacket, 16)
	controlSendChan := make(chan *protocol.GamePacket, 16)

	t := &ClientTransport{
		FromTun:         outerChan,
		FromNet:         innerChan,
		ControlRecvChan: controlRecvChan,
		ControlSendChan: controlSendChan,
		sendPacketCount: sendPacketCount,
		recvPacketCount: recvPacketCount,
		status:          status,
		bufPool: &sync.Pool{New: func() interface{} {
			return make([]byte, 2048)
		}},
		board: ui.NewBoard(status, sendPacketCount, recvPacketCount),
	}

	// 延迟到 ListenAndServe 中初始化的资源
	t.noiseMgr = protocol.NewNoiseManager(outerChan)
	t.p2pRouter.Store(make(map[[4]byte]*stun.P2PStatus))

	if config.EnableUi {
		go t.board.PerformanceUi()
	}

	return t
}

// ListenAndServe 启动客户端服务并包含自动重连机制
func (t *ClientTransport) ListenAndServe(ctx context.Context, testFlag bool, testSecond *time.Duration) {
	t.initUi()

	// 1. 启动持久层协程 (不受网络断开影响)
	persistentGroup, pCtx := errgroup.WithContext(ctx)

	// Noise 会话清理
	persistentGroup.Go(func() error {
		return t.noiseMgr.CheckSession(pCtx)
	})

	// UI 状态清理
	if config.EnableUi {
		persistentGroup.Go(func() error {
			<-pCtx.Done()
			atomic.StoreUint32(t.status, ui.CLOSING)
			return nil
		})
	}

	// 2. 主重连循环
	persistentGroup.Go(func() error {
		var retryCount int
		for {
			// A. 基础探测与连接
			if config.EnableP2P {
				atomic.StoreUint32(t.status, ui.TESTNAT) // 状态：探测 NAT
				natType, err := stun.GetNatType()
				if err == nil {
					t.natType = natType
				}
			}

			atomic.StoreUint32(t.status, ui.CONNECT) // 状态：连接服务器
			err := t.connectServer()
			if err != nil {
				log.Printf("连接服务器失败 (重试 %d): %v", retryCount, err)
				if waitErr := t.backoffWait(pCtx, &retryCount); waitErr != nil {
					return waitErr
				}
				continue
			}

			// B. 握手获取虚拟 IP
			atomic.StoreUint32(t.status, ui.INTINETWORK) // 状态：握手
			handshake, err := t.handshake()
			if err != nil {
				log.Printf("握手失败 (重试 %d): %v", retryCount, err)
				t.dataConn.Close()
				if waitErr := t.backoffWait(pCtx, &retryCount); waitErr != nil {
					return waitErr
				}
				continue
			}

			// C. 同步虚拟 IP 与 TUN 设备
			newVip := handshake.Destination().To4()
			if t.localIp == nil || !t.localIp.Equal(newVip) {
				t.localIp = newVip
				t.noiseMgr.SetVirtualIp(util.IpToKey(t.localIp))
				t.board.InitLocalIp(util.IpToKey(t.localIp))

				if t.device != nil {
					t.device.Close()
				}
				t.device = tun.NewTun(config.DeviceName, t.localIp, t.FromTun, t.FromNet, t.bufPool)
				// 启动 TUN 协程
				go t.device.Start(pCtx, protocol.HeaderLength)
			}
			t.bufPool.Put(handshake.RawData[:0])

			// D. 运行会话协程
			retryCount = 0                           // 连接成功，重置重试计数
			atomic.StoreUint32(t.status, ui.RUNNING) // 状态：运行中
			log.Printf("连接成功，虚拟 IP: %s", t.localIp.String())

			sessionGroup, sCtx := errgroup.WithContext(pCtx)

			sessionGroup.Go(func() error { return t.controlRecv(sCtx) })
			sessionGroup.Go(func() error { return t.controlSend(sCtx) })
			sessionGroup.Go(func() error { return t.heartbeat(sCtx) })

			for i := 0; i < config.RecvWorkers; i++ {
				sessionGroup.Go(func() error { return t.packetRecv(sCtx) })
			}
			for i := 0; i < config.SendWorkers; i++ {
				sessionGroup.Go(func() error { return t.packetSend(sCtx) })
			}
			if config.EnableP2P {
				sessionGroup.Go(func() error { return t.handleP2P(sCtx) })
			}
			if testFlag {
				sessionGroup.Go(func() error { return t.testBroadCast(sCtx, *testSecond) })
			}

			// 监听 Context 取消信号，主动关闭连接以打断阻塞读取
			sessionGroup.Go(func() error {
				<-sCtx.Done()
				t.dataConn.Close()
				t.controlConn.Close()
				return nil
			})

			// 等待当前会话结束 (网络断开或 Context 取消)
			if err := sessionGroup.Wait(); err != nil {
				log.Printf("网络连接断开: %v", err)
				atomic.StoreUint32(t.status, ui.RECONNECTING)
			}

			// 清理当前连接
			t.dataConn.Close()
			t.controlConn.Close()

			select {
			case <-pCtx.Done():
				return nil
			default:
				// 继续循环重连
			}
		}
	})

	if err := persistentGroup.Wait(); err != nil {
		log.Printf("持久服务停止: %v", err)
	}
}

// backoffWait 实现指数退避等待
func (t *ClientTransport) backoffWait(ctx context.Context, retryCount *int) error {
	atomic.StoreUint32(t.status, ui.RECONNECTING)
	*retryCount++
	waitSec := 1 << uint(*retryCount)
	if waitSec > 30 {
		waitSec = 30
	}

	log.Printf("%d 秒后尝试重连...", waitSec)
	timer := time.NewTimer(time.Duration(waitSec) * time.Second)
	defer timer.Stop()

	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
