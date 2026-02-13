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
	"golang.org/x/net/ipv4"
)

type ClientTransport struct {
	device tun.Device

	FromTun chan []byte
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
	t.localIp = net.IPv4(handshake.Payload[0], handshake.Payload[1], handshake.Payload[2], handshake.Payload[3])
	t.bufPool.Put(handshake.RawData[:0])
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
	data := handshakePacket.EncodePacket(t.bufPool)
	err := t.controlConn.WriteMessage(websocket.BinaryMessage, data)
	t.bufPool.Put(data[:0])
	if err != nil {
		return nil, err
	}
	t.controlConn.SetReadDeadline(time.Now().Add(time.Second * config.ReadTimeout))
	_, content, err := t.controlConn.ReadMessage()
	if err != nil {
		return nil, err
	}
	err = handshakePacket.ParsePacket(t.bufPool, content, true)
	if err != nil {
		return nil, err
	}
	return handshakePacket, nil
}

// ListenAndServe 监听服务
func (t *ClientTransport) ListenAndServe(ctx context.Context, cancel context.CancelFunc, testFlag bool, testSecond *time.Duration) {

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
		log.Printf("已启用 %d 个接收者\n", config.RecvWorkers)
		for i := 0; i < config.RecvWorkers; i++ {
			go t.packetRecv(ctx)
		}
		select {
		case <-ctx.Done():
		}

	}()
	go func() {
		defer wg.Done()
		defer log.Println(5)
		log.Printf("已启用 %d 个发送者\n", config.SendWorkers)
		for i := 0; i < config.SendWorkers; i++ {
			go t.packetSend(ctx)
		}
		select {
		case <-ctx.Done():
		}

	}()
	go func() {
		defer wg.Done()
		defer log.Println(6)
		t.device.Start(ctx, protocol.HeaderLength)
	}()
	if testFlag {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer log.Println(5)
			t.testBroadCast(ctx, *testSecond)
		}()
	}
	wg.Wait()
}

// heartbeat 心跳消息
func (t *ClientTransport) heartbeat(ctx context.Context) {
	timer := time.NewTicker(time.Second * config.PingTime)
	defer timer.Stop()
	hearbeatPacket := protocol.NewGamePacket([4]byte(t.localIp.To4()), [4]byte{}, protocol.TypePing, nil)
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
		//gp := &protocol.GamePacket{}
		t.controlConn.SetReadDeadline(time.Now().Add(time.Second * config.ReadTimeout))
		t.controlConn.SetPongHandler(func(string) error {
			//log.Println("pong")
			t.controlConn.SetReadDeadline(time.Now().Add(time.Second * config.ReadTimeout))
			return nil
		})
		_, _, err := t.controlConn.ReadMessage()
		if err != nil {
			log.Println(err)
			break
		}
		// TODO 处理控制消息
		//data := t.bufPool.Get().([]byte)
		//if cap(data) < len(content) {
		//	t.bufPool.Put(data[:0])
		//	data = make([]byte, len(content))
		//}
		//data = data[:len(content)]
		//copy(data, content)
		//err = gp.Parse(data, true)
		//if err != nil {
		//	log.Println(err)
		//	break
		//}
		//select {
		//case t.ControlRecvChan <- gp:
		//	continue
		//case <-ctx.Done():
		//	t.bufPool.Put(data[:0])
		//	return
		//default:
		//	t.bufPool.Put(data[:0])
		//	continue
		//}
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
			data := gp.EncodePacket(t.bufPool)
			err := t.controlConn.WriteMessage(websocket.BinaryMessage, data)
			t.bufPool.Put(data[:0])
			if err != nil {
				log.Println(err)
				break
			}
		case <-ctx.Done():
			return
		}
	}
}

// packetSend 封包并发送
func (t *ClientTransport) packetSend(ctx context.Context) {
	batchSize := 32
	payloadBatch := make([][]byte, 0, batchSize)
	for {
		select {
		case pr := <-t.FromTun:
			if len(pr) < 20 {
				continue
			}
			payloadBatch = append(payloadBatch, pr)
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
				gp := protocol.NewGamePacket([4]byte(t.localIp.To4()), [4]byte(dstIp), protocol.TypeData, p)
				data := gp.EncodePacket(t.bufPool)
				_, err := t.dataConn.Write(data)
				t.bufPool.Put(p[:0])
				t.bufPool.Put(data)
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
			err = gp.ParsePacket(t.bufPool, buffer[:cnt], true)
			if err != nil {
				log.Println(err)
				continue
			}
			//log.Printf("收到 %s 的消息\n", gp.SourceVirtualIp())
			packetEnd := protocol.HeaderLength + gp.Length
			if cnt < int(packetEnd) {
				log.Println(errorcode.PayloadMismatch)
				t.bufPool.Put(gp.RawData[:0])
				continue
			}
			select {
			case t.FromNet <- gp.RawData:
			case <-ctx.Done():
				return
			default:
				t.bufPool.Put(gp.RawData[:0])
				//log.Println("FromNet 已满")
			}
		}
	}
}

func (t *ClientTransport) testBroadCast(ctx context.Context, second time.Duration) {
	timer := time.NewTicker(time.Second * second)
	broadCastHeader := &ipv4.Header{
		Version:  ipv4.Version,
		Len:      ipv4.HeaderLen,
		TOS:      0x0,
		TotalLen: ipv4.HeaderLen,
		TTL:      64,
		Protocol: 17,            // UDP
		Dst:      net.IPv4bcast, // 255.255.255.255
		Src:      t.localIp,     // 你的虚拟 IP
	}
	bch, err := broadCastHeader.Marshal()
	if err != nil {
		panic(err)
	}
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			log.Println("执行广播...")
			t.FromTun <- bch
		case <-ctx.Done():
			return
		}
	}
}
