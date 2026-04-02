//go:build server

package transport

import (
	"easytun/internal/protocol"
	"easytun/internal/stun"
	"easytun/internal/util"
	"math/rand/v2"
	"net"
	"sync"
)

// NewHub 创建并初始化一个新的 Hub 实例
func NewHub() *Hub {
	subnet := &net.IPNet{
		IP:   net.IPv4(10, 0, 6, 1),
		Mask: net.IPv4Mask(255, 255, 255, 0),
	}
	h := &Hub{
		controlChan:  make(chan *protocol.GamePacket, 16),
		transferChan: make(chan *transferPacket, 128),
		packetChan:   make(chan *packet, 128),
		p2pTaskChan:  make(chan stun.P2PTask, 32),
		mtx:          sync.Mutex{},
		tm:           stun.NewTunnelManager(),
		ipMtx:        sync.Mutex{},
		ipBitMap:     make(map[uint8]struct{}),
		Subnet:       subnet,
		bufPool:      &sync.Pool{New: func() interface{} { return make([]byte, 2048) }},
		tpPool:       &sync.Pool{New: func() interface{} { return &transferPacket{gp: &protocol.GamePacket{}} }},
		packetPool:   &sync.Pool{New: func() interface{} { return &packet{} }},
	}
	// 初始化路由表快照
	eSnapshot := &routerSnapshot{
		clientMap:   make(map[[4]byte]*Client),
		clientSlice: make([]*Client, 0),
	}
	h.router.Store(eSnapshot)
	return h
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

// getIp 在虚拟子网内为新客户端随机分配一个未使用的虚拟 IP
func (h *Hub) getIp() net.IP {
	// 随机起始位置以减少碰撞
	startIdx := uint8(rand.Uint32()%250 + 2)

	// 遍历子网可用地址 (10.0.6.2 到 10.0.6.251)
	for i := 0; i < 250; i++ {
		idx := ((startIdx - 2 + uint8(i)) % 250) + 2
		if _, ok := h.ipBitMap[idx]; !ok {
			h.ipBitMap[idx] = struct{}{}
			return net.IPv4(10, 0, 6, idx)
		}
	}
	// 无空闲 IP 时返回 nil
	return nil
}
