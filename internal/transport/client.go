package transport

import (
	"context"
	"errors"
	"fmt"
	"game_tun/internal/config"
	"game_tun/internal/errorcode"
	"game_tun/internal/protocol"
	"game_tun/internal/tun"
	"log"
	"net"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type ClientTransport struct {
	device tun.Device

	FromTun <-chan []byte
	FromNet chan<- []byte

	// 服务器收发队列
	ControlRecvChan chan *protocol.GamePacket
	ControlSendChan chan *protocol.GamePacket

	controlConn *websocket.Conn
	dataConn    *net.UDPConn
	serverAddr  *net.UDPAddr

	localIp net.IP
}

func NewTransport() *ClientTransport {
	outerChan := make(chan []byte, 64)
	innerChan := make(chan []byte, 64)
	controlRecvChan := make(chan *protocol.GamePacket, 16)
	controlSendChan := make(chan *protocol.GamePacket, 16)
	t := &ClientTransport{
		FromTun:         outerChan,
		FromNet:         innerChan,
		ControlRecvChan: controlRecvChan,
		ControlSendChan: controlSendChan,
	}
	err := t.connectServer()
	if err != nil {
		log.Fatal(err)
	}
	handshake, err := t.handshake()
	if err != nil {
		log.Fatal(err)
	}
	t.localIp = handshake.Payload[:4]
	device := tun.NewTun(config.DeviceName, t.localIp, outerChan, innerChan)
	t.device = device
	return t
}

// connectServer 创建连接
func (t *ClientTransport) connectServer() error {
	wsUrl := fmt.Sprintf("ws://%s:%d/ws", config.SeverIp, config.SeverPort)
	tempControlConn, _, err := websocket.DefaultDialer.Dial(wsUrl, nil)
	if err != nil {
		return err
	}
	t.controlConn = tempControlConn

	serverAddr, _ := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", config.SeverIp, config.SeverPort))
	t.serverAddr = serverAddr
	conn, err := net.DialUDP("udp", nil, serverAddr)
	if err != nil {
		return err
	}
	t.dataConn = conn
	return nil
}

// handshake 与服务器握手连接
func (t *ClientTransport) handshake() (*protocol.GamePacket, error) {
	handshakePacket := protocol.NewGamePacket([4]byte{}, [4]byte{}, protocol.TypeHandshake, nil)
	err := t.controlConn.WriteMessage(websocket.BinaryMessage, handshakePacket.Encode())
	if err != nil {
		return nil, err
	}
	t.controlConn.SetReadDeadline(time.Now().Add(time.Second * config.ReadTimeout))
	_, content, err := t.controlConn.ReadMessage()
	if err != nil {
		return nil, err
	}
	err = handshakePacket.Parse(content, true)
	if err != nil {
		return nil, err
	}
	return handshakePacket, nil
}

// ListenAndServe 监听服务
func (t *ClientTransport) ListenAndServe() {
	ctx, cancel := context.WithCancel(context.Background())
	wg := sync.WaitGroup{}
	wg.Add(6)

	go func() {
		defer wg.Done()
		defer log.Println(1)
		t.controlRecv(ctx)
	}()
	go func() {
		defer wg.Done()
		defer log.Println(2)
		t.controlSend(ctx)
		cancel()
	}()
	go func() {
		defer wg.Done()
		defer log.Println(3)
		t.heartbeat(ctx)
	}()
	go func() {
		defer wg.Done()
		defer log.Println(4)
		t.packetRecv(ctx)
	}()
	go func() {
		defer wg.Done()
		defer log.Println(5)
		t.packetSend(ctx)
	}()
	go func() {
		defer wg.Done()
		defer log.Println(6)
		t.device.Start(ctx)
	}()
	wg.Wait()
}

// heartbeat 心跳消息
func (t *ClientTransport) heartbeat(ctx context.Context) {
	timer := time.NewTicker(time.Second * config.PingTime)
	defer timer.Stop()
	hearbeatPacket := protocol.NewGamePacket([4]byte(t.localIp), [4]byte{}, protocol.TypePing, nil)
	for {
		select {
		case <-timer.C:
			t.ControlSendChan <- hearbeatPacket
			//log.Println("发送ping")
		case <-ctx.Done():
			return
		}
	}
}

// controlRecv 接收控制消息
func (t *ClientTransport) controlRecv(ctx context.Context) {
	defer t.controlConn.Close()
	for ctx.Err() == nil {
		gp := &protocol.GamePacket{}
		t.controlConn.SetReadDeadline(time.Now().Add(time.Second * config.ReadTimeout * 3))
		t.controlConn.SetPongHandler(func(string) error {
			//log.Println("pong")
			t.controlConn.SetReadDeadline(time.Now().Add(time.Second * config.ReadTimeout * 3))
			return nil
		})
		_, content, err := t.controlConn.ReadMessage()
		if err != nil {
			log.Println(err)
			break
		}
		err = gp.Parse(content, true)
		if err != nil {
			log.Println(err)
			break
		}
		select {
		case t.ControlRecvChan <- gp:
			continue
		case <-ctx.Done():
			return
		}
	}
}

// controlSend 发送控制消息
func (t *ClientTransport) controlSend(ctx context.Context) {
	for {
		select {
		case gp := <-t.ControlSendChan:
			if gp.PType == protocol.TypePing {
				err := t.controlConn.WriteMessage(websocket.PingMessage, nil)
				if err != nil {
					log.Println(err)
					return
				}
			}
			err := t.controlConn.WriteMessage(websocket.BinaryMessage, gp.Encode())
			if err != nil {
				log.Println(err)
				break
			}
		case <-ctx.Done():
		}
	}
}

// packetSend 封包并发送
func (t *ClientTransport) packetSend(ctx context.Context) {
	for {
		select {
		case packet := <-t.FromTun:
			if len(packet) < 20 {
				continue
			}
			dstIp := net.IP(packet[16:20])
			gp := protocol.NewGamePacket([4]byte(t.localIp), [4]byte(dstIp), protocol.TypeData, packet)
			_, err := t.dataConn.Write(gp.Encode())
			if err != nil {
				log.Println(err)
				continue
			}
		case <-ctx.Done():
			return
		}
	}
}

// packetRecv 接收并解包
func (t *ClientTransport) packetRecv(ctx context.Context) {
	buffer := make([]byte, 2048)
	for ctx.Err() == nil {
		t.dataConn.SetReadDeadline(time.Now().Add(time.Second * config.ReadTimeout))
		cnt, addr, err := t.dataConn.ReadFromUDP(buffer)
		if err != nil {
			var netErr *net.OpError
			if errors.As(err, &netErr) && netErr.Timeout() {
				continue
			}
			log.Println(err)
			continue
		}
		if addr.IP.Equal(t.serverAddr.IP) && addr.Port == t.serverAddr.Port {
			gp := &protocol.GamePacket{}
			err = gp.Parse(buffer[:cnt], false)
			if err != nil {
				log.Println(err)
				continue
			}
			packetEnd := protocol.HeaderLength + gp.Length
			if cnt < int(packetEnd) {
				log.Println(errorcode.PayloadMismatch)
				continue
			}
			dataCopy := make([]byte, gp.Length)
			copy(dataCopy, buffer[protocol.HeaderLength:packetEnd])
			select {
			case t.FromNet <- dataCopy:
			case <-ctx.Done():
				return
			}
		}
	}
}
