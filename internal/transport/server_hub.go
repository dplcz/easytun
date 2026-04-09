//go:build server

package transport

import (
	"context"
	"easytun/internal/protocol"
	"easytun/internal/stun"
	"easytun/internal/util"
	"net"
	"sync"
)

// NewHub 创建并初始化一个新的 Hub 实例
func NewHub() *Hub {
	subnet := &net.IPNet{
		IP:   net.IPv4(10, 0, 6, 1),
		Mask: net.IPv4Mask(255, 255, 255, 0),
	}

	// 初始化空闲 IP 栈 (10.0.6.2 到 10.0.6.254)
	freeIps := make([]uint8, 0, 253)
	for i := 254; i >= 2; i-- {
		freeIps = append(freeIps, uint8(i))
	}

	h := &Hub{
		controlChan:  make(chan *protocol.GamePacket, 16),
		transferChan: make(chan *transferPacket, 128),
		packetChan:   make(chan *packet, 128),
		p2pTaskChan:  make(chan stun.P2PTask, 32),
		mtx:          sync.Mutex{},
		tm:           stun.NewTunnelManager(),
		ipMtx:        sync.Mutex{},
		freeIps:      freeIps,
		Subnet:       subnet,
		bufPool:      &sync.Pool{New: func() interface{} { return make([]byte, 2048) }},
		tpPool:       &sync.Pool{New: func() interface{} { return &transferPacket{gp: &protocol.GamePacket{}} }},
		packetPool:   &sync.Pool{New: func() interface{} { return &packet{} }},
		wsHandlers:   make(map[uint8]WSHandler),
	}
	// 注册默认处理器
	h.registerDefaultWSHandlers()
	// 初始化路由表快照
	eSnapshot := &routerSnapshot{
		clientMap:   make(map[[4]byte]*Client),
		clientSlice: make([]*Client, 0),
	}

	dSnapshot := &dnsSnapshot{dnsMap: make(map[string]net.IP)}

	h.router.Store(eSnapshot)
	h.dnsMap.Store(dSnapshot)
	return h
}

// RegisterWSHandler 注册 WebSocket 控制消息处理器
func (h *Hub) RegisterWSHandler(pType uint8, handler WSHandler) {
	h.wsMtx.Lock()
	defer h.wsMtx.Unlock()
	h.wsHandlers[pType] = handler
}

// HandleWSMessage 分发并处理 WebSocket 控制消息
func (h *Hub) HandleWSMessage(ctx context.Context, c *Client, gp *protocol.GamePacket) error {
	h.wsMtx.RLock()
	handler, ok := h.wsHandlers[gp.PType]
	h.wsMtx.RUnlock()

	if !ok {
		// 未知消息类型不处理或记录日志
		return nil
	}
	return handler(ctx, c, gp)
}

// addClient 向 Hub 中添加一个新客户端并更新路由表快照
func (h *Hub) addClient(client *Client) {
	h.mtx.Lock()
	defer h.mtx.Unlock()
	oldSnapshot := h.router.Load().(*routerSnapshot)

	// 创建新的映射和切片以实现无锁读取（Copy-On-Write）
	newMap := make(map[[4]byte]*Client, len(oldSnapshot.clientMap)+1)
	newSlice := make([]*Client, 0, len(oldSnapshot.clientMap)+1)

	for k, v := range oldSnapshot.clientMap {
		newMap[k] = v
		newSlice = append(newSlice, v)
	}
	newMap[util.IpToKey(client.virtualIp)] = client
	newSlice = append(newSlice, client)
	newSnapshot := &routerSnapshot{
		clientMap:   newMap,
		clientSlice: newSlice,
	}
	h.router.Store(newSnapshot)
}

// removeClient 从 Hub 中移除一个客户端并更新路由快照
func (h *Hub) removeClient(client *Client) {
	h.mtx.Lock()
	defer h.mtx.Unlock()

	oldSnapshot := h.router.Load().(*routerSnapshot)
	vIp := util.IpToKey(client.virtualIp)
	if _, exists := oldSnapshot.clientMap[vIp]; !exists {
		return
	}

	newMap := make(map[[4]byte]*Client, len(oldSnapshot.clientMap))
	newSlice := make([]*Client, 0, len(oldSnapshot.clientMap))
	for k, v := range oldSnapshot.clientMap {
		if k != vIp {
			newMap[k] = v
			newSlice = append(newSlice, v)
		}
	}
	newSnapshot := &routerSnapshot{
		clientMap:   newMap,
		clientSlice: newSlice,
	}
	h.router.Store(newSnapshot)
	// 同时移除该客户端相关的 P2P 隧道
	h.tm.RemoveA(util.IpToKey(client.virtualIp))
}

// getIp 在虚拟子网内从空闲栈中分配一个未使用的虚拟 IP (O(1))
func (h *Hub) getIp() net.IP {
	h.ipMtx.Lock()
	defer h.ipMtx.Unlock()

	n := len(h.freeIps)
	if n == 0 {
		return nil
	}

	// 从栈顶弹出
	last := h.freeIps[n-1]
	h.freeIps = h.freeIps[:n-1]

	// 组装完整的 IPv4 地址
	res := make(net.IP, 4)
	copy(res, h.Subnet.IP.To4())
	res[3] = last
	return res
}

// releaseIp 回收已释放的 IP 到空闲栈中 (O(1))
func (h *Hub) releaseIp(ip net.IP) {
	v4 := ip.To4()
	if v4 == nil {
		return
	}

	h.ipMtx.Lock()
	defer h.ipMtx.Unlock()

	// 回收到栈中
	h.freeIps = append(h.freeIps, v4[3])
}
