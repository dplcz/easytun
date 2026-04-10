//go:build server

package transport

import (
	"context"
	"easytun/internal/config"
	"easytun/internal/protocol"
	"easytun/internal/stun"
	"easytun/internal/util"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/ipv4"

	"github.com/gorilla/websocket"
)

// upgrader 用于将 HTTP 连接升级为 WebSocket 连接
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	// 允许跨域 (生产环境建议限制)
	CheckOrigin: func(r *http.Request) bool { return true },
}

// packet 代表待发送的数据包及其目标信息
type packet struct {
	data      []byte         // 数据负载
	broadcast bool           // 是否为广播包
	dstAddr   netip.AddrPort // 目标 UDP 地址 (使用 netip.AddrPort 优化)
}

// Client 代表一个已连接的客户端
type Client struct {
	natType uint8 // 客户端的 NAT 类型

	hub *Hub // 所属的 Hub 实例

	controlConn *websocket.Conn           // WebSocket 控制连接
	controlChan chan *protocol.GamePacket // 发送给客户端的控制消息队列

	dataAddr   atomic.Value // 客户端最新的 UDP 数据端口地址 (存储 netip.AddrPort)
	packetChan chan *packet // 待发送给该客户端的数据包队列

	virtualIp      net.IP   // 客户端的虚拟 IP 地址
	hostname       string   // 客户端的虚拟 hostname
	noisePublicKey [32]byte // 客户端的 Noise 协议公钥
	clientID       [16]byte // 客户端持久化唯一 ID
}

// transferPacket 代表从中转队列接收到的待处理数据包
type transferPacket struct {
	gp      *protocol.GamePacket // 解析后的协议包
	srcAddr netip.AddrPort       // 来源 UDP 地址
}

// routerSnapshot 代表客户端路由表的快照，用于原子读取
type routerSnapshot struct {
	clientMap   map[[4]byte]*Client // 虚拟 IP 到 Client 的映射
	clientSlice []*Client           // 客户端列表切片，方便遍历
}

type dnsSnapshot struct {
	dnsMap map[string]net.IP
}

// offlineClient 代表处于保留期内的离线客户端信息
type offlineClient struct {
	virtualIp net.IP
	hostname  string
	expiry    time.Time
}

// Hub 是服务端的核心管理器，处理路由、地址分配和数据中转
type Hub struct {
	PacketConn   *ipv4.PacketConn // 批量发送 UDP 的连接封装
	UdpConn      *net.UDPConn     // 主 UDP 数据连接
	CheckUdpConn *net.UDPConn     // P2P Check 专用 UDP 连接

	controlChan  chan *protocol.GamePacket // 系统级控制消息队列
	transferChan chan *transferPacket      // 待中转的数据包队列
	packetChan   chan *packet              // 待下发到 UDP 的数据包队列
	p2pTaskChan  chan stun.P2PTask         // P2P 调度任务队列

	router atomic.Value        // 存放 routerSnapshot
	tm     *stun.TunnelManager // P2P 隧道管理器
	mtx    sync.Mutex          // 路由表修改锁

	dnsMap atomic.Value // 存放 dnsSnapshot
	dnsMtx sync.Mutex   // dns 表修改锁

	ipMtx   sync.Mutex // IP 分配锁
	freeIps []uint8    // 空闲 IP 地址池 (Stack)

	retentionMap map[[16]byte]*offlineClient // 处于保留期的离线客户端 ID -> 信息
	retentionMtx sync.Mutex                  // 保留期映射表锁

	Subnet     *net.IPNet // 虚拟网段信息
	bufPool    *sync.Pool // 字节缓冲区对象池
	tpPool     *sync.Pool // transferPacket 对象池
	packetPool *sync.Pool // packet 对象池

	wsHandlers map[uint8]WSHandler // WebSocket 消息处理器映射
	wsMtx      sync.RWMutex        // 处理器映射读写锁
}

// WSHandler 定义了处理 WebSocket 控制消息的函数原型
type WSHandler func(ctx context.Context, c *Client, gp *protocol.GamePacket) error

// Run 启动服务端的所有核心服务
func (h *Hub) Run(ctx context.Context) {
	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		h.serverWS(ctx, w, r)
	})
	go h.listenUdp(ctx)
	go h.cleanRetention(ctx)
	log.Printf("开始监听WS... 端口:%d", config.ServerPort)
	err := http.ListenAndServe(fmt.Sprintf(":%d", config.ServerPort), nil)
	if err != nil {
		log.Fatalf("HTTP 服务启动失败: %v", err)
	}
}

// transfer 是数据中转的核心逻辑，负责根据目标 IP 进行路由分发
func (h *Hub) transfer(ctx context.Context) {
	for {
		select {
		case tp := <-h.transferChan:
			consume := false
			dst := tp.gp.Destination()
			src := tp.gp.SourceVirtualIp()
			snapshot := h.router.Load().(*routerSnapshot)
			srcIp := util.IpToKey(src)
			srcClient, ok := snapshot.clientMap[srcIp]
			if ok {
				srcClient.updateAddrCheck(tp.srcAddr)
			}
			switch {
			case dst.Equal(net.IPv4bcast) || dst.To4()[3] == 255 || dst.IsMulticast():
				// 处理广播/多播包
				continue
			case h.Subnet.Contains(dst) && !dst.IsLoopback():
				// 处理虚拟网段内的单播包
				dstIp := util.IpToKey(dst.To4())
				client, ok := snapshot.clientMap[dstIp]
				if ok {
					dstAddr, _ := client.dataAddr.Load().(netip.AddrPort)
					// 尝试判断是否需要开启 P2P
					if h.ifEstablish(srcIp, dstIp, srcClient.natType, client.natType) {
						h.newP2PTask(ctx, srcIp, dstIp, tp.srcAddr, dstAddr)
					}
					// 将数据包放入发送队列
					p := h.packetPool.Get().(*packet)
					p.data = tp.gp.RawData
					p.broadcast = false
					p.dstAddr = dstAddr
					select {
					case h.packetChan <- p:
						consume = true
					case <-ctx.Done():
						h.packetPool.Put(p)
						return
					default:
						log.Println("packetChan已满")
						h.packetPool.Put(p)
					}
				} else {
					log.Println("未找到目标客户端:", dstIp)
				}
			}
			if !consume {
				h.bufPool.Put(tp.gp.RawData[:0])
			}
			tp.gp.RawData = nil
			h.tpPool.Put(tp)
		case <-ctx.Done():
			return
		}
	}
}

// updateAddrCheck 检测并更新客户端的公网 UDP 数据地址
func (c *Client) updateAddrCheck(addr netip.AddrPort) {
	oldAddr, _ := c.dataAddr.Load().(netip.AddrPort)
	if oldAddr == addr {
		return
	}
	log.Printf("客户端地址更新: %s, 之前: %v, 现在: %v\n", c.virtualIp.String(), oldAddr, addr)
	c.dataAddr.Store(addr)
}
