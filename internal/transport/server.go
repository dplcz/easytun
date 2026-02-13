package transport

import (
	"context"
	"errors"
	"fmt"
	"game_tun/internal/config"
	"game_tun/internal/errorcode"
	"game_tun/internal/protocol"
	"log"
	"math/rand/v2"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/ipv4"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	// 允许跨域 (生产环境建议限制)
	CheckOrigin: func(r *http.Request) bool { return true },
}

type packet struct {
	data  []byte
	total int32
	cnt   int32
}

type Client struct {
	hub *Hub

	controlConn *websocket.Conn
	controlChan chan *protocol.GamePacket

	dataAddr   atomic.Pointer[net.UDPAddr]
	packetChan chan *packet

	virtualIp net.IP
}

type transferPacket struct {
	gp      *protocol.GamePacket
	srcAddr *net.UDPAddr
}

type Hub struct {
	PacketConn *ipv4.PacketConn
	UdpConn    *net.UDPConn

	controlChan  chan *protocol.GamePacket
	transferChan chan *transferPacket

	Router map[string]*Client
	mtx    sync.RWMutex

	ipMtx    sync.Mutex
	ipBitMap map[uint8]struct{}

	Subnet  *net.IPNet
	bufPool *sync.Pool
}

func newClient(hub *Hub, controlConn *websocket.Conn, dataAddr *net.UDPAddr, virtualIp net.IP) *Client {
	client := &Client{
		hub:         hub,
		controlConn: controlConn,
		controlChan: make(chan *protocol.GamePacket, 16),
		packetChan:  make(chan *packet, 128),
		virtualIp:   virtualIp,
	}
	client.dataAddr.Store(dataAddr)
	return client
}

func NewHub() *Hub {
	subnet := &net.IPNet{
		IP:   net.IPv4(10, 0, 6, 1),
		Mask: net.IPv4Mask(255, 255, 255, 0),
	}
	return &Hub{
		controlChan:  make(chan *protocol.GamePacket, 16),
		transferChan: make(chan *transferPacket, 64),
		Router:       make(map[string]*Client),
		mtx:          sync.RWMutex{},
		ipMtx:        sync.Mutex{},
		ipBitMap:     make(map[uint8]struct{}),
		Subnet:       subnet,
		bufPool:      &sync.Pool{New: func() interface{} { return make([]byte, 2048) }},
	}
}

func (c *Client) writePump(ctx context.Context, cancel context.CancelFunc) {
	for {
		select {
		case gp, ok := <-c.controlChan:
			if !ok {
				// Channel 被关闭，优雅关闭连接
				c.controlConn.WriteMessage(websocket.CloseMessage, []byte{})
				cancel()
				return
			}
			if gp.PType == protocol.TypePong {
				c.controlConn.WriteMessage(websocket.PongMessage, nil)
				continue
			}
			w, err := c.controlConn.NextWriter(websocket.BinaryMessage)
			if err != nil {
				log.Println(err)
				cancel()
				return
			}
			data := gp.EncodePacket(c.hub.bufPool)
			w.Write(data)
			c.hub.bufPool.Put(data[:0])
			if err := w.Close(); err != nil {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

func (c *Client) readPump(ctx context.Context, cancel context.CancelFunc) {
	pongGp := protocol.NewGamePacket([4]byte{}, [4]byte{}, protocol.TypePong, nil)
	c.controlConn.SetPingHandler(func(string) error {
		c.controlConn.SetReadDeadline(time.Now().Add(config.ReadTimeout * time.Second * 3))
		c.controlChan <- pongGp
		return nil
	})
	gp := &protocol.GamePacket{}
	for ctx.Err() == nil {
		msgType, message, err := c.controlConn.ReadMessage()
		if err != nil {
			var netErr *net.OpError
			if errors.As(err, &netErr) && netErr.Timeout() {
				continue
			}
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("error: %v", err)
			}
			cancel()
			break
		}
		if msgType == websocket.BinaryMessage {
			var newGp *protocol.GamePacket

			err = gp.ParsePacket(c.hub.bufPool, message, true)
			if err != nil {
				log.Println(err)
				continue
			}
			switch gp.PType {
			case protocol.TypeHandshake:
				c.hub.mtx.Lock()
				c.hub.Router[c.virtualIp.String()] = c
				c.hub.mtx.Unlock()
				newGp = protocol.NewGamePacket([4]byte{}, [4]byte{}, protocol.TypeHandshake, c.virtualIp.To4())
			default:
				continue
			}
			select {
			case c.controlChan <- newGp:
				c.hub.bufPool.Put(gp.RawData[:0])
			case <-ctx.Done():
				return
			default:
				c.hub.bufPool.Put(gp.RawData[:0])
				continue
			}
		}
	}
}

func (c *Client) writeUdpPacket(ctx context.Context) {
	batchSize := 32
	msgs := make([]ipv4.Message, batchSize)
	packetBatch := make([]*packet, 0, batchSize)
	for {
		select {
		case pr := <-c.packetChan:
			packetBatch = append(packetBatch, pr)
		DrainLoop:
			for len(packetBatch) < batchSize {
				select {
				case extraPacket := <-c.packetChan:
					packetBatch = append(packetBatch, extraPacket)
				default:
					break DrainLoop
				}
			}

			targetAddr := c.dataAddr.Load()
			if targetAddr != nil {
				for i, p := range packetBatch {
					msgs[i].Buffers = [][]byte{p.data} // 设置数据
					msgs[i].Addr = targetAddr          // 设置目标地址
				}
				_, err := c.hub.PacketConn.WriteBatch(msgs[:len(packetBatch)], 0)
				if err != nil {
					log.Println(err)
				}
			}
			for _, p := range packetBatch {
				if atomic.AddInt32(&p.cnt, 1) == p.total {
					c.hub.bufPool.Put(p.data[:0])
				}
			}
			packetBatch = packetBatch[:0]
		case <-ctx.Done():
			for {
				p, ok := <-c.packetChan
				if !ok {
					return
				}
				if atomic.AddInt32(&p.cnt, 1) == p.total {
					log.Println("----", p.cnt, p.total)
					c.hub.bufPool.Put(p.data[:0])
				}
			}
		}
	}
}

func (c *Client) updateAddrCheck(addr *net.UDPAddr) {
	oldAddr := c.dataAddr.Load()
	if oldAddr == nil || !oldAddr.IP.Equal(addr.IP) {
		log.Printf("%s 地址 %s 更改为 %s\n", c.virtualIp.String(), oldAddr.String(), addr.String())
		c.dataAddr.Store(addr)
	}
}

func (h *Hub) Run(ctx context.Context) {
	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		h.serverWS(ctx, w, r)
	})
	go h.listenUdp(ctx)
	log.Printf("开始监听WS... 端口:%d", config.ServerPort)
	err := http.ListenAndServe(fmt.Sprintf(":%d", config.ServerPort), nil)
	if err != nil {
		log.Fatalf("HTTP 服务启动失败: %v", err)
	}
}

func (h *Hub) serverWS(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	log.Println("接收到 WebSocket 连接")
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("升级 WebSocket 失败:", err)
		return
	}

	// 创建新用户实例
	h.ipMtx.Lock()
	ip := h.getIp()
	h.ipMtx.Unlock()
	client := newClient(h, conn, nil, ip)
	newCtx, cancel := context.WithCancel(ctx)
	// 启动该用户的读写协程
	go client.writePump(newCtx, cancel) // WS 写
	go client.readPump(newCtx, cancel)  // WS 读

	go client.writeUdpPacket(newCtx)
	select {
	case <-newCtx.Done():
		log.Printf("断开 %s 连接\n", client.virtualIp.String())
		h.ipMtx.Lock()
		delete(h.ipBitMap, client.virtualIp[3])
		h.ipMtx.Unlock()
		h.mtx.Lock()
		close(client.packetChan)
		delete(h.Router, client.virtualIp.String())
		h.mtx.Unlock()
		return
	}
}

func (h *Hub) handleControl(ctx context.Context) {
	for {
		select {
		case pg := <-h.controlChan:
			switch pg.PType {
			case protocol.TypeHandshake:

			}
		case <-ctx.Done():
			return
		}
	}
}

func (h *Hub) listenUdp(ctx context.Context) {
	udpAddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf(":%d", config.ServerPort))
	if err != nil {
		log.Fatalf("UDP 地址解析失败: %v", err)
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		log.Fatalf("UDP 监听失败: %v", err)
	}
	conn.SetReadBuffer(4 * 1024 * 1024)  // 4MB
	conn.SetWriteBuffer(4 * 1024 * 1024) // 4MB

	h.PacketConn = ipv4.NewPacketConn(conn)
	h.UdpConn = conn
	batchSize := 64
	msgs := make([]ipv4.Message, batchSize)
	go h.transfer(ctx)
	log.Println("开始监听UDP...")
	for i := range msgs {
		msgs[i].Buffers = [][]byte{make([]byte, 2048)}
	}
	for ctx.Err() == nil {
		h.UdpConn.SetReadDeadline(time.Now().Add(config.ReadTimeout * time.Second))
		count, err := h.PacketConn.ReadBatch(msgs, 0)
		if err != nil {
			var netErr *net.OpError
			if errors.As(err, &netErr) && netErr.Timeout() {
				continue
			}
			log.Println(err)
			continue
		}
		for i := 0; i < count; i++ {
			msg := msgs[i]
			cnt := msg.N                       // 这个包的实际字节数
			srcAddr := msg.Addr.(*net.UDPAddr) // 对方地址
			payload := msg.Buffers[0][:cnt]
			gp := &protocol.GamePacket{}
			err = gp.ParsePacket(h.bufPool, payload, true)
			if err != nil {
				log.Println(err)
				continue
			}
			if cnt < int(gp.Length) {
				log.Println(errorcode.PayloadMismatch)
				continue
			}
			if gp.PType != protocol.TypeData {
				log.Println(errorcode.PayloadMismatch)
				continue
			}
			tp := &transferPacket{
				gp:      gp,
				srcAddr: srcAddr,
			}
			select {
			case h.transferChan <- tp:
			case <-ctx.Done():
				return
			default:
				log.Println("transferChan已满")
			}
		}
	}
}

func (h *Hub) transfer(ctx context.Context) {
	for {
		select {
		case tp := <-h.transferChan:
			dst := tp.gp.Destination()
			src := tp.gp.SourceVirtualIp()
			h.mtx.RLock()
			srcClient, ok := h.Router[src.String()]
			h.mtx.RUnlock()
			if ok {
				srcClient.updateAddrCheck(tp.srcAddr)
			}
			switch {
			case dst.Equal(net.IPv4bcast) || dst.To4()[3] == 255 || dst.IsMulticast():
				h.mtx.RLock()
				if len(h.Router) < 2 {
					h.mtx.RUnlock()
					continue
				}
				p := &packet{
					cnt:   0,
					total: int32(len(h.Router)) - 1,
					data:  tp.gp.RawData,
				}
				for virtualIp, client := range h.Router {
					if virtualIp == src.String() {
						continue
					}
					select {
					case client.packetChan <- p:
					case <-ctx.Done():
						h.bufPool.Put(p.data[:0])
						h.mtx.RUnlock()
						close(client.packetChan)
						return
					default:
						if atomic.AddInt32(&p.cnt, 1) == p.total {
							h.bufPool.Put(p.data[:0])
						}
					}
				}
				h.mtx.RUnlock()
			case h.Subnet.Contains(dst) && !dst.IsLoopback():
				h.mtx.RLock()
				client, ok := h.Router[dst.String()]
				h.mtx.RUnlock()
				if ok {
					p := &packet{
						cnt:   0,
						total: 1,
						data:  tp.gp.RawData,
					}
					select {
					case client.packetChan <- p:
					case <-ctx.Done():
						h.bufPool.Put(p.data[:0])
						close(client.packetChan)
						return
					default:
						log.Println("dataChan已满")
						h.bufPool.Put(p.data[:0])
					}
				}
			}
		case <-ctx.Done():
			return
		}
	}
}

func (h *Hub) getIp() net.IP {
	idx := uint8(rand.Uint()%250 + 2)
	skip := uint8(1)
	_, ok := h.ipBitMap[idx]
	if !ok {
		h.ipBitMap[idx] = struct{}{}
		return net.IPv4(10, 0, 6, idx)
	}
	for {
		idx = (idx+skip)%250 + 2
		_, ok = h.ipBitMap[idx+skip]
		if !ok {
			h.ipBitMap[idx] = struct{}{}
			return net.IPv4(10, 0, 6, idx)
		}
		skip++
	}
}
