//go:build server

package transport

import (
	"bytes"
	"context"
	"easytun/internal/config"
	"easytun/internal/protocol"
	"easytun/internal/stun"
	"easytun/internal/util"
	"errors"
	"log"
	"net"
	"net/http"
	"net/netip"
	"time"

	"github.com/gorilla/websocket"
)

// newClient 创建并初始化一个 Client 实例
func newClient(hub *Hub, controlConn *websocket.Conn, dataAddr netip.AddrPort, virtualIp net.IP) *Client {
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

// writePump 将待发送的控制消息通过 WebSocket 持续发送给客户端
func (c *Client) writePump(ctx context.Context, cancel context.CancelFunc) {
	for {
		select {
		case gp, ok := <-c.controlChan:
			if !ok {
				// 发送关闭控制帧
				c.controlConn.WriteMessage(websocket.CloseMessage, []byte{})
				cancel()
				return
			}
			// 特殊处理心跳响应
			if gp.PType == protocol.TypePong {
				c.controlConn.WriteMessage(websocket.PongMessage, nil)
				continue
			}
			w, err := c.controlConn.NextWriter(websocket.BinaryMessage)
			if err != nil {
				log.Println("WebSocket 写出错误:", err)
				cancel()
				return
			}
			data := gp.EncodePacket(c.hub.bufPool, true, false, nil)
			w.Write(data)
			c.hub.bufPool.Put(data[:0])
			// 如果是 NoiseResponse，清理 Payload 缓存
			if gp.PType == protocol.TypeNoiseResponse {
				c.hub.bufPool.Put(gp.Payload[:0])
			}
			if err := w.Close(); err != nil {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// readPump 持续从 WebSocket 读取客户端发来的控制消息并分发处理
func (c *Client) readPump(ctx context.Context, cancel context.CancelFunc) {
	pongGp := protocol.NewGamePacket([4]byte{}, [4]byte{}, protocol.TypePong, nil)
	c.controlConn.SetPingHandler(func(string) error {
		// 收到 Ping 后重置超时时间并回复 Pong
		c.controlConn.SetReadDeadline(time.Now().Add(config.ReadTimeout))
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
				log.Printf("WebSocket 意外关闭: %v", err)
			}
			cancel()
			break
		}
		if msgType == websocket.BinaryMessage {
			readBuffer.Reset()
			_, err = readBuffer.ReadFrom(reader)
			if err != nil {
				log.Println("读取 WebSocket 数据流失败:", err)
				continue
			}

			err = gp.ParseControl(readBuffer.Bytes())
			if err != nil {
				log.Println("解析控制消息失败:", err)
				continue
			}
			if err := c.hub.HandleWSMessage(ctx, c, gp); err != nil {
				log.Printf("处理消息类型 %d 失败: %v", gp.PType, err)
			}
		}
	}
}

// registerDefaultWSHandlers 注册系统内置的 WebSocket 消息处理器
func (h *Hub) registerDefaultWSHandlers() {
	h.RegisterWSHandler(protocol.TypeHandshake, h.handleHandshake)
	h.RegisterWSHandler(protocol.TypeNoiseHandshake, h.handleNoiseHandshake)
	h.RegisterWSHandler(protocol.TypeP2PEstablished, h.handleP2PEstablished)
	h.RegisterWSHandler(protocol.TypeP2PClosed, h.handleP2PClosed)
}

// handleHandshake 处理客户端初始连接握手
func (h *Hub) handleHandshake(ctx context.Context, c *Client, gp *protocol.GamePacket) error {
	/*
		TODO 实现DNS
		BASE
		1.hostname和ip的解析表
		2.udp读取DNS请求并返回响应
		FEAT
		1.client本地缓存
		2.client提前续期
	*/
	dSnapshot := h.dnsMap.Load().(*dnsSnapshot)
	c.natType = gp.Payload[0]
	c.noisePublicKey = [32]byte(gp.Payload[1:33])
	c.hostname = util.UniqueHostName(string(gp.Payload[33:]), dSnapshot.dnsMap)
	log.Printf("新客户端接入 - Virtual Ip: %s, NAT Type: %d, Host Name: %s", c.virtualIp.String(), c.natType, c.hostname)

	c.hub.addClient(c)
	c.hub.addDns(c.hostname, c.virtualIp)
	// 回复握手确认
	newGp := protocol.NewGamePacket([4]byte{}, util.IpToKey(c.virtualIp), protocol.TypeHandshake, []byte(c.hostname))
	select {
	case c.controlChan <- newGp:
	case <-ctx.Done():
	default:
	}
	return nil
}

// handleNoiseHandshake 中转 Noise 协议握手包
func (h *Hub) handleNoiseHandshake(ctx context.Context, c *Client, gp *protocol.GamePacket) error {
	snapshot := h.router.Load().(*routerSnapshot)
	dstC, ok := snapshot.clientMap[util.IpToKey(gp.Destination())]
	if !ok {
		return nil
	}
	pendingBuffer := h.bufPool.Get().([]byte)
	pendingBuffer = pendingBuffer[:0]
	pendingBuffer = append(pendingBuffer, dstC.noisePublicKey[:]...)
	pendingBuffer = append(pendingBuffer, gp.Payload...)
	newGp := protocol.NewGamePacket(util.IpToKey(gp.Destination()), util.IpToKey(c.virtualIp), protocol.TypeNoiseResponse, pendingBuffer)
	select {
	case c.controlChan <- newGp:
	case <-ctx.Done():
	default:
		h.bufPool.Put(pendingBuffer[:0])
	}
	return nil
}

// handleP2PEstablished 处理 P2P 隧道建立成功的通知
func (h *Hub) handleP2PEstablished(ctx context.Context, c *Client, gp *protocol.GamePacket) error {
	A, B := util.IpToKey(c.virtualIp), util.IpToKey(gp.Destination())
	t, ok := h.tm.Exist(A, B)
	if !ok {
		h.tm.AddTunnel(A, B, stun.TunnelConnected)
	} else if t.GetStatus() == stun.TunnelInit {
		log.Println("P2P 通道建立成功:", A, B)
		t.ChangeStatus(stun.TunnelConnected)
	}
	return nil
}

// handleP2PClosed 处理 P2P 隧道关闭或失败的通知
func (h *Hub) handleP2PClosed(ctx context.Context, c *Client, gp *protocol.GamePacket) error {
	A, B := util.IpToKey(c.virtualIp), util.IpToKey(gp.Destination())
	t, ok := h.tm.Exist(A, B)
	if ok {
		log.Println("P2P 通道关闭:", A, B)
		t.ChangeStatus(stun.TunnelFailed)
		t.AddRetryTimes()
	}
	return nil
}

// serverWS 处理来自客户端的 WebSocket 升级请求并启动维护协程
func (h *Hub) serverWS(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("HTTP 升级为 WebSocket 失败:", err)
		return
	}

	ip := h.getIp()
	if ip == nil {
		log.Println("无法为新连接分配虚拟 IP")
		return
	}
	client := newClient(h, conn, netip.AddrPort{}, ip)
	newCtx, cancel := context.WithCancel(ctx)

	// 启动该用户的读写逻辑
	go client.writePump(newCtx, cancel)
	go client.readPump(newCtx, cancel)

	select {
	case <-newCtx.Done():
		log.Printf("客户端断开连接: %s\n", client.virtualIp.String())
		// 回收 IP 和清理状态
		h.releaseIp(client.virtualIp)
		h.removeClient(client)
		h.removeDns(client.hostname)

		h.tm.RemoveA(util.IpToKey(client.virtualIp))
		return
	}
}
