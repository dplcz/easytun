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
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	// 允许跨域 (生产环境建议限制)
	CheckOrigin: func(r *http.Request) bool { return true },
}

type Client struct {
	hub *Hub

	controlConn *websocket.Conn
	controlChan chan *protocol.GamePacket

	dataAddr *net.UDPAddr
	dataChan chan []byte

	virtualIp net.IP
}

type transferPacket struct {
	gp      *protocol.GamePacket
	srcAddr *net.UDPAddr
}

type Hub struct {
	UdpConn *net.UDPConn

	controlChan  chan *protocol.GamePacket
	transferChan chan *transferPacket

	Router map[string]*Client
	mtx    sync.RWMutex
	// TODO 可能要加锁
	IpBitMap map[uint8]struct{}

	Subnet *net.IPNet
}

func newClient(hub *Hub, controlConn *websocket.Conn, dataAddr *net.UDPAddr, virtualIp net.IP) *Client {
	return &Client{
		hub:         hub,
		controlConn: controlConn,
		controlChan: make(chan *protocol.GamePacket, 16),
		dataAddr:    dataAddr,
		dataChan:    make(chan []byte, 32),
		virtualIp:   virtualIp,
	}
}

func newHub() *Hub {
	subnet := &net.IPNet{
		IP:   net.IPv4(10, 0, 6, 1),
		Mask: net.IPv4Mask(255, 255, 255, 0),
	}
	return &Hub{
		controlChan:  make(chan *protocol.GamePacket, 16),
		transferChan: make(chan *transferPacket, 64),
		Router:       make(map[string]*Client),
		mtx:          sync.RWMutex{},
		IpBitMap:     make(map[uint8]struct{}),
		Subnet:       subnet,
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
			w.Write(gp.Encode())
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
		log.Printf("收到 %s ping\n", c.virtualIp.String())
		c.controlChan <- pongGp
		return nil
	})
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
			gp := &protocol.GamePacket{}
			err = gp.Parse(message, true)
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
			case <-ctx.Done():
				return
			}
		}
	}
}

func (c *Client) writeUdpPacket(ctx context.Context) {
	for {
		select {
		case packet := <-c.dataChan:
			_, err := c.hub.UdpConn.WriteToUDP(packet, c.dataAddr)
			if err != nil {
				log.Println(err)
				continue
			}
		case <-ctx.Done():
			return
		}
	}
}

func (c *Client) updateAddr(addr *net.UDPAddr) {
	c.dataAddr = addr
}

func (h *Hub) Run(ctx context.Context) {
	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		h.serverWS(ctx, w, r)
	})
	go h.listenUdp(ctx)
	err := http.ListenAndServe(fmt.Sprintf(":%d", config.SeverPort), nil)
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
	ip := h.getIp()
	client := newClient(h, conn, nil, ip)
	newCtx, cancel := context.WithCancel(ctx)
	// 启动该用户的读写协程
	go client.writePump(newCtx, cancel) // WS 写
	go client.readPump(newCtx, cancel)  // WS 读
	select {
	case <-newCtx.Done():
		log.Printf("断开 %s 连接\n", client.virtualIp.String())
		delete(h.IpBitMap, client.virtualIp[3])
		delete(h.Router, client.virtualIp.String())
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
	udpAddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf(":%d", config.SeverPort))
	if err != nil {
		log.Fatalf("UDP 地址解析失败: %v", err)
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		log.Fatalf("UDP 监听失败: %v", err)
	}
	h.UdpConn = conn
	go h.transfer(ctx)
	buf := make([]byte, 2048)
	gp := &protocol.GamePacket{}
	for ctx.Err() == nil {
		h.UdpConn.SetReadDeadline(time.Now().Add(config.ReadTimeout * time.Second))
		cnt, clientAddr, err := h.UdpConn.ReadFromUDP(buf)
		if err != nil {
			var netErr *net.OpError
			if errors.As(err, &netErr) && netErr.Timeout() {
				continue
			}
			log.Println(err)
			continue
		}
		err = gp.Parse(buf[:cnt], true)
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
			srcAddr: clientAddr,
		}
		select {
		case h.transferChan <- tp:
		case <-ctx.Done():
			return
		}
	}
}

func (h *Hub) transfer(ctx context.Context) {
	for {
		select {
		case tp := <-h.transferChan:
			dst := tp.gp.Destination()
			switch {
			case dst.Equal(net.IPv4bcast) || dst.To4()[3] == 255 || dst.IsMulticast():
				// 广播
				//log.Println("广播")
			case h.Subnet.Contains(dst) && !dst.IsLoopback():
				h.mtx.RLock()
				client, ok := h.Router[dst.String()]
				if ok {
					_, err := h.UdpConn.WriteToUDP(tp.gp.Encode(), client.dataAddr)
					if err != nil {
						log.Println(err)
						break
					}
				}
				h.mtx.RUnlock()
			}
		case <-ctx.Done():
			return
		}
	}

}

func (h *Hub) getIp() net.IP {
	idx := uint8(rand.Uint()%250 + 2)
	skip := uint8(1)
	_, ok := h.IpBitMap[idx]
	if !ok {
		h.IpBitMap[idx] = struct{}{}
		return net.IPv4(10, 0, 6, idx)
	}
	for {
		idx = (idx+skip)%250 + 2
		_, ok = h.IpBitMap[idx+skip]
		if !ok {
			h.IpBitMap[idx] = struct{}{}
			return net.IPv4(10, 0, 6, idx)
		}
		skip *= 2
	}
}
