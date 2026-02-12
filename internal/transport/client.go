//go:build client

package transport

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"game_tun/internal/config"
	"game_tun/internal/errorcode"
	"game_tun/internal/protocol"
	"game_tun/internal/tun"
	"game_tun/internal/util"
	"log"
	"net"
	"os"
	"runtime/debug"
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
	bufPool *sync.Pool
}

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

	outerChan := make(chan []byte, 64)
	innerChan := make(chan []byte, 64)
	controlRecvChan := make(chan *protocol.GamePacket, 16)
	controlSendChan := make(chan *protocol.GamePacket, 16)
	t := &ClientTransport{
		FromTun:         outerChan,
		FromNet:         innerChan,
		ControlRecvChan: controlRecvChan,
		ControlSendChan: controlSendChan,
		bufPool: &sync.Pool{New: func() interface{} {
			return make([]byte, 2048)
		}},
	}

	err := t.connectServer()
	if err != nil {
		panic(err)
	}
	handshake, err := t.handshake()
	if err != nil {
		panic(err)
	}
	t.localIp = handshake.Payload[:4]
	device := tun.NewTun(config.DeviceName, t.localIp, outerChan, innerChan, t.bufPool)
	t.device = device
	return t
}

// connectServer 创建连接
func (t *ClientTransport) connectServer() error {
	rtt, loss := util.TestPing(config.ServerIp)
	log.Printf("与服务器延迟为 %v , 丢包率为 %v%%\n", rtt, loss)

	wsUrl := fmt.Sprintf("ws://%s:%d/ws", config.ServerIp, config.ServerPort)
	tempControlConn, _, err := websocket.DefaultDialer.Dial(wsUrl, nil)
	if err != nil {
		return err
	}
	t.controlConn = tempControlConn

	serverAddr, _ := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", config.ServerIp, config.ServerPort))
	t.serverAddr = serverAddr
	conn, err := net.DialUDP("udp", nil, serverAddr)
	conn.SetReadBuffer(4 * 1024 * 1024)
	conn.SetWriteBuffer(4 * 1024 * 1024)
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
	batchSize := 64
	payloadBatch := make([][]byte, 0, batchSize)
	for {
		select {
		case packet := <-t.FromTun:
			if len(packet) < 20 {
				continue
			}
			payloadBatch = append(payloadBatch, packet)
		DrainLoop:
			for len(payloadBatch) < batchSize {
				select {
				case extraPacket := <-t.FromTun:
					if len(extraPacket) < 20 {
						continue
					}
					payloadBatch = append(payloadBatch, extraPacket)
				default:
					break DrainLoop
				}
			}
			for _, p := range payloadBatch {
				dstIp := net.IP(p[16:20])
				gp := protocol.NewGamePacket([4]byte(t.localIp), [4]byte(dstIp), protocol.TypeData, p)
				_, err := t.dataConn.Write(gp.Encode())
				t.bufPool.Put(p[:0])
				if err != nil {
					log.Println(err)
					continue
				}
			}
			payloadBatch = payloadBatch[:0]
		case <-ctx.Done():
			return
		}
	}
}

// packetRecv 接收并解包
func (t *ClientTransport) packetRecv(ctx context.Context) {
	buffer := make([]byte, 4*1024*1024)
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
			dataCopy := make([]byte, cnt)
			copy(dataCopy, buffer[:cnt])
			err = gp.Parse(dataCopy, false)
			if err != nil {
				log.Println(err)
				continue
			}
			//log.Printf("收到 %s 的消息\n", gp.SourceVirtualIp())
			packetEnd := protocol.HeaderLength + gp.Length
			if cnt < int(packetEnd) {
				log.Println(errorcode.PayloadMismatch)
				continue
			}
			select {
			case t.FromNet <- dataCopy[protocol.HeaderLength:packetEnd]:
			case <-ctx.Done():
				return
			default:
				log.Println("FromNet 已满")
			}
		}
	}
}
