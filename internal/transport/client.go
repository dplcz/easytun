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

	FromTun chan []byte   // 从 TUN 设备接收的数据通道
	FromNet chan<- []byte // 写入 TUN 设备的数据通道

	// 服务器收发队列
	ControlRecvChan chan *protocol.GamePacket // 控制消息接收队列
	ControlSendChan chan *protocol.GamePacket // 控制消息发送队列

	controlConn *websocket.Conn // WebSocket 控制连接封装 (为了隐藏具体实现)
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

// NewTransport 创建并初始化客户端传输层实例
func NewTransport() *ClientTransport {
	// TODO 优化程序状态显示
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

	if config.EnableUi {
		go t.board.PerformanceUi()
	}
	var natType uint8
	var err error
	if config.EnableP2P {
		atomic.AddUint32(t.status, 1)
		natType, err = stun.GetNatType()
		if err != nil {
			panic(err)
		}
		atomic.AddUint32(t.status, 1)
	} else {
		natType = stun.TypeUnknown
		atomic.AddUint32(t.status, 2)
	}
	t.natType = natType
	t.noiseMgr = protocol.NewNoiseManager(outerChan)
	err = t.connectServer()
	if err != nil {
		atomic.AddUint32(t.status, 3)
		panic(err)
	}
	handshake, err := t.handshake()
	if err != nil {
		atomic.AddUint32(t.status, 3)
		panic(err)
	}
	t.localIp = handshake.Destination().To4()
	t.noiseMgr.SetVirtualIp(util.IpToKey(t.localIp))
	t.board.InitLocalIp(util.IpToKey(t.localIp))
	t.bufPool.Put(handshake.RawData[:0])
	atomic.AddUint32(t.status, 1)
	device := tun.NewTun(config.DeviceName, t.localIp, outerChan, innerChan, t.bufPool)
	t.device = device
	t.p2pRouter.Store(make(map[[4]byte]*stun.P2PStatus))
	return t
}

// ListenAndServe 启动客户端的所有核心服务协程，使用 errgroup 管理生命周期
func (t *ClientTransport) ListenAndServe(ctx context.Context, cancel context.CancelFunc, testFlag bool, testSecond *time.Duration) {
	t.initUi()

	g, gCtx := errgroup.WithContext(ctx)

	// 控制平面：WebSocket 接收
	g.Go(func() error {
		return t.controlRecv(gCtx)
	})

	// 控制平面：WebSocket 发送
	g.Go(func() error {
		err := t.controlSend(gCtx)
		cancel() // 发送协程退出意味着连接断开，取消 context
		return err
	})

	// 维护：心跳
	g.Go(func() error {
		return t.heartbeat(gCtx)
	})

	// 数据平面：接收者们
	for i := 0; i < config.RecvWorkers; i++ {
		g.Go(func() error {
			return t.packetRecv(gCtx)
		})
	}

	// 数据平面：发送者们
	for i := 0; i < config.SendWorkers; i++ {
		g.Go(func() error {
			return t.packetSend(gCtx)
		})
	}

	// TUN 设备
	g.Go(func() error {
		t.device.Start(gCtx, protocol.HeaderLength)
		return nil
	})

	// Noise 会话清理
	g.Go(func() error {
		return t.noiseMgr.CheckSession(gCtx)
	})

	// 测试广播
	if testFlag {
		g.Go(func() error {
			return t.testBroadCast(gCtx, *testSecond)
		})
	}

	// P2P 维护
	if config.EnableP2P {
		g.Go(func() error {
			return t.handleP2P(gCtx)
		})
	}

	// UI 状态清理
	if config.EnableUi {
		g.Go(func() error {
			<-gCtx.Done()
			atomic.AddUint32(t.status, 1)
			return nil
		})
	}

	atomic.AddUint32(t.status, 1)

	if err := g.Wait(); err != nil {
		log.Printf("服务由于错误而停止: %v", err)
	}
}
