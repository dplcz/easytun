//go:build server

package transport

import (
	"bytes"
	"context"
	"easytun/internal/config"
	"easytun/internal/errorcode"
	"easytun/internal/protocol"
	"easytun/internal/stun"
	"easytun/internal/util"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"net"
	"net/http"
	"runtime"
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
	data      []byte
	broadcast bool
	dstAddr   *net.UDPAddr
}

type Client struct {
	natType uint8

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

type routerSnapshot struct {
	clientMap   map[[4]byte]*Client
	clientSlice []*Client
}

type Hub struct {
	PacketConn *ipv4.PacketConn
	UdpConn    *net.UDPConn

	controlChan  chan *protocol.GamePacket
	transferChan chan *transferPacket
	packetChan   chan *packet
	p2pTaskChan  chan stun.P2PTask

	router atomic.Value
	tm     *stun.TunnelManager
	mtx    sync.Mutex

	ipMtx    sync.Mutex
	ipBitMap map[uint8]struct{}

	Subnet     *net.IPNet
	bufPool    *sync.Pool
	tpPool     *sync.Pool
	packetPool *sync.Pool
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
	h := &Hub{
		controlChan:  make(chan *protocol.GamePacket, 16),
		transferChan: make(chan *transferPacket, 64),
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
	// 初始化快照
	eSnapshot := &routerSnapshot{
		clientMap:   make(map[[4]byte]*Client),
		clientSlice: make([]*Client, 0),
	}
	h.router.Store(eSnapshot)
	return h
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
			data := gp.EncodePacket(c.hub.bufPool, true)
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
		c.controlConn.SetReadDeadline(time.Now().Add(config.ReadTimeout * time.Second))
		c.controlChan <- pongGp
		return nil
	})
	gp := &protocol.GamePacket{}
	readBuffer := bytes.NewBuffer(make([]byte, 512))
	for ctx.Err() == nil {
		msgType, reader, err := c.controlConn.NextReader()
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
			readBuffer.Reset()
			_, err = readBuffer.ReadFrom(reader) // ReadFrom 会一直读到 EOF，底层自动扩容/复用内存
			if err != nil {
				log.Println("Read buffer error:", err)
				continue
			}
			var newGp *protocol.GamePacket

			err = gp.ParseControl(readBuffer.Bytes())
			if err != nil {
				log.Println(err)
				continue
			}
			switch gp.PType {
			case protocol.TypeHandshake:
				if len(gp.Payload) < 1 {
					c.natType = stun.TypeUnknown
				} else {
					c.natType = gp.Payload[0]
					log.Printf("Virtual Ip: %s ,NAT type: %d", c.virtualIp.String(), c.natType)
				}
				c.hub.addClient(c)
				newGp = protocol.NewGamePacket([4]byte{}, util.IpToKey(c.virtualIp), protocol.TypeHandshake, nil)
			case protocol.TypeP2PEstablished:
				A, B := util.IpToKey(c.virtualIp), util.IpToKey(gp.Destination())
				t, ok := c.hub.tm.Exist(A, B)
				if !ok {
					c.hub.tm.AddTunnel(A, B, stun.TunnelConnected)
					continue
				} else if t.GetStatus() == stun.TunnelInit {
					log.Println("修改 p2p 通道状态", A, B)
					t.ChangeStatus(1)
				}
			case protocol.TypeP2PClosed:
				A, B := util.IpToKey(c.virtualIp), util.IpToKey(gp.Destination())
				c.hub.tm.RemoveTunnel(A, B)
			default:
				continue
			}
			select {
			case c.controlChan <- newGp:
			case <-ctx.Done():
				return
			default:
				continue
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

func (h *Hub) addClient(client *Client) {
	h.mtx.Lock()
	defer h.mtx.Unlock()
	oldSnapshot := h.router.Load().(*routerSnapshot)

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
}

func (h *Hub) serverWS(ctx context.Context, w http.ResponseWriter, r *http.Request) {
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

	select {
	case <-newCtx.Done():
		log.Printf("断开 %s 连接\n", client.virtualIp.String())
		h.ipMtx.Lock()
		delete(h.ipBitMap, client.virtualIp[3])
		h.ipMtx.Unlock()
		h.removeClient(client)

		h.tm.RemoveA(util.IpToKey(client.virtualIp))
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

	workerCount := runtime.NumCPU() * 2

	for i := 0; i < 2; i++ {
		go h.transfer(ctx)
	}
	log.Println("启动 2 个处理协程")
	for i := 0; i < workerCount; i++ {
		go h.writeUdpPacket(ctx)
	}
	log.Printf("启动 %d 个发送协程\n", workerCount)
	log.Println("启动 p2p 调度协程")
	go h.handleP2PTask(ctx)

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

			consume := false
			tp := h.tpPool.Get().(*transferPacket)
			tp.srcAddr = srcAddr
			gp := tp.gp
			err = gp.ParsePacket(h.bufPool, payload, true)
			if err != nil {
				log.Println(err)
				goto CleanUp
			}
			if cnt < int(gp.Length) {
				log.Println(errorcode.PayloadMismatch)
				goto CleanUp
			}
			if gp.PType != protocol.TypeData {
				log.Println(errorcode.PayloadMismatch)
				goto CleanUp
			}
			select {
			case h.transferChan <- tp:
				consume = true
			case <-ctx.Done():
				h.bufPool.Put(tp.gp.RawData[:0])
				h.tpPool.Put(tp)
				return
			default:
				log.Println("transferChan已满")
				goto CleanUp
			}
		CleanUp:
			if !consume {
				h.bufPool.Put(tp.gp.RawData[:0])
				gp.RawData = nil
				tp.srcAddr = nil
				h.tpPool.Put(tp)
			}
		}
	}
}

// 处理中转
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
				if len(snapshot.clientSlice) < 2 {
					break
				}
				p := h.packetPool.Get().(*packet)
				p.data = tp.gp.RawData
				p.broadcast = true
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
			case h.Subnet.Contains(dst) && !dst.IsLoopback():
				dstIp := util.IpToKey(dst.To4())
				client, ok := snapshot.clientMap[dstIp]
				if ok {
					dstAddr := client.dataAddr.Load()
					if h.ifEstablish(srcIp, dstIp, srcClient.natType, client.natType) {
						h.newP2PTask(ctx, srcIp, dstIp, tp.srcAddr, dstAddr)
					}
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
				}
			}
			if !consume {
				h.bufPool.Put(tp.gp.RawData[:0])
			}
			tp.gp.RawData = nil
			tp.srcAddr = nil
			h.tpPool.Put(tp)
		case <-ctx.Done():
			return
		}
	}
}

func (h *Hub) writeUdpPacket(ctx context.Context) {
	batchSize := 16
	msgs := make([]ipv4.Message, 0, batchSize*4)
	packetBatch := make([]*packet, 0, batchSize)
	for {
		select {
		case pa := <-h.packetChan:
			packetBatch = append(packetBatch, pa)
		DrainLoop:
			for len(packetBatch) < batchSize {
				select {
				case extraPacket := <-h.packetChan:
					packetBatch = append(packetBatch, extraPacket)
				default:
					break DrainLoop
				}
			}
			snapshot := h.router.Load().(*routerSnapshot)
			for _, p := range packetBatch {
				if p.broadcast {
					bBuf := [][]byte{p.data}
					for _, client := range snapshot.clientSlice {
						addr := client.dataAddr.Load()
						if addr == nil {
							continue
						}
						msgs = append(msgs, ipv4.Message{
							Buffers: bBuf,
							Addr:    addr,
						})
					}
				} else {
					msgs = append(msgs, ipv4.Message{
						Buffers: [][]byte{p.data},
						Addr:    p.dstAddr,
					})
				}
			}
			_, err := h.PacketConn.WriteBatch(msgs, 0)
			if err != nil {
				log.Println(err)
			}
			for i, p := range packetBatch {
				h.bufPool.Put(p.data[:0])
				p.data = nil
				p.dstAddr = nil
				h.packetPool.Put(p)
				packetBatch[i] = nil
			}
			packetBatch = packetBatch[:0]
			msgs = msgs[:0]
		case <-ctx.Done():
			return
		}
	}
}

// 处理P2P
func (h *Hub) ifEstablish(src, dst [4]byte, srcNat, dstNat uint8) bool {
	tunnel, ok := h.tm.Exist(src, dst)
	if !ok {
		return true
	}
	if tunnel.GetStatus() == stun.TunnelFailed && tunnel.GetRetryTimes() < 3 {
		return true
	}
	return false
}

func (h *Hub) newP2PTask(ctx context.Context, srcVip, dstVip [4]byte, srcAddr, dstAddr *net.UDPAddr) {
	newTask := stun.P2PTask{
		DstVip: dstVip,
		Dst:    dstAddr,
		SrcVip: srcVip,
		Src:    srcAddr,
	}
	select {
	case h.p2pTaskChan <- newTask:
		log.Println("创建新的P2P任务 From: ", srcVip, "to", dstVip)
	case <-ctx.Done():
		return
	default:
		log.Println("创建P2P通道请求已满...")
	}
}

func (h *Hub) handleP2PTask(ctx context.Context) {
	var srcClient, dstClient *Client
	var snapShot *routerSnapshot
	var t *stun.P2PTunnel
	var srcOk, dstOk, ok bool
	for {
		select {
		case task := <-h.p2pTaskChan:
			snapShot = h.router.Load().(*routerSnapshot)
			t, ok = h.tm.Exist(task.SrcVip, task.DstVip)
			if !ok {
				h.tm.AddTunnel(task.SrcVip, task.DstVip, stun.TunnelInit)
			} else {
				t.AddRetryTimes()
			}
			srcClient, srcOk = snapShot.clientMap[task.SrcVip]
			dstClient, dstOk = snapShot.clientMap[task.DstVip]
			if !srcOk || !dstOk {
				continue
			}
			srcClient.controlChan <- protocol.NewGamePacket(task.DstVip, task.SrcVip, protocol.TypeP2PCommand, util.UDPAddrToBytes(dstClient.dataAddr.Load(), dstClient.natType))
			dstClient.controlChan <- protocol.NewGamePacket(task.SrcVip, task.DstVip, protocol.TypeP2PCommand, util.UDPAddrToBytes(srcClient.dataAddr.Load(), srcClient.natType))
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
