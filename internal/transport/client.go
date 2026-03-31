//go:build client

package transport

import (
	"bufio"
	"bytes"
	"context"
	"easytun/internal/config"
	"easytun/internal/crypt"
	"easytun/internal/errorcode"
	"easytun/internal/protocol"
	"easytun/internal/stun"
	"easytun/internal/tun"
	"easytun/internal/ui"
	"easytun/internal/util"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/net/ipv4"
)

type ClientTransport struct {
	natType         uint8
	sendPacketCount *uint64
	recvPacketCount *uint64
	status          *uint32

	device tun.Device

	FromTun chan []byte
	FromNet chan<- []byte

	// 服务器收发队列
	ControlRecvChan chan *protocol.GamePacket
	ControlSendChan chan *protocol.GamePacket

	controlConn *websocket.Conn
	dataConn    *net.UDPConn
	serverAddr  *net.UDPAddr
	checkAddr   *net.UDPAddr

	p2pRouter atomic.Value
	routerMtx sync.Mutex

	localIp net.IP
	bufPool *sync.Pool

	noiseMgr *crypt.NoiseManager
}

func NewTransport() *ClientTransport {
	// TODO 优化程序状态显示
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
	sendPacketCount := new(uint64)
	recvPacketCount := new(uint64)
	status := new(uint32)
	outerChan := make(chan []byte, 256)
	innerChan := make(chan []byte, 64)
	controlRecvChan := make(chan *protocol.GamePacket, 16)
	controlSendChan := make(chan *protocol.GamePacket, 16)
	t := &ClientTransport{
		FromTun:         outerChan,
		FromNet:         innerChan,
		ControlRecvChan: controlRecvChan,
		ControlSendChan: controlSendChan,
		sendPacketCount: sendPacketCount,
		recvPacketCount: recvPacketCount,
		status:          status,
		bufPool: &sync.Pool{New: func() interface{} {
			return make([]byte, 2048)
		}},
	}

	if config.EnableUi {
		go ui.PerformanceUi(t.sendPacketCount, t.recvPacketCount, t.status)
	}

	var natType uint8
	var err error
	if config.EnableP2P {
		atomic.AddUint32(t.status, 1)
		natType, err = stun.GetNatType()
		if err != nil {
			panic(err)
		}
		atomic.AddUint32(t.status, 1)
	} else {
		natType = stun.TypeUnknown
		atomic.AddUint32(t.status, 2)
	}
	t.natType = natType
	t.noiseMgr = crypt.NewNoiseManager()
	err = t.connectServer()
	if err != nil {
		atomic.AddUint32(t.status, 3)
		panic(err)
	}
	handshake, err := t.handshake()
	if err != nil {
		atomic.AddUint32(t.status, 3)
		panic(err)
	}
	t.localIp = handshake.Destination().To4()
	t.noiseMgr.SetVirtualIp(util.IpToKey(t.localIp))
	t.bufPool.Put(handshake.RawData[:0])
	atomic.AddUint32(t.status, 1)
	device := tun.NewTun(config.DeviceName, t.localIp, outerChan, innerChan, t.bufPool)
	t.device = device
	t.p2pRouter.Store(make(map[[4]byte]*stun.P2PStatus))
	return t
}

// connectServer 创建连接
func (t *ClientTransport) connectServer() error {
	//rtt, loss := util.TestPing(config.ServerIp)
	//log.Printf("与服务器延迟为 %v , 丢包率为 %v%%\n", rtt, loss)

	wsUrl := fmt.Sprintf("ws://%s:%d/ws", config.ServerIp, config.ServerPort)
	tempControlConn, _, err := websocket.DefaultDialer.Dial(wsUrl, nil)
	if err != nil {
		return err
	}
	t.controlConn = tempControlConn

	serverAddr, _ := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", config.ServerIp, config.ServerPort))
	t.serverAddr = serverAddr
	checkAddr, _ := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", config.ServerIp, config.CheckPort))
	t.checkAddr = checkAddr
	localAddr, _ := net.ResolveUDPAddr("udp", ":0")
	conn, err := net.ListenUDP("udp", localAddr)
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
	payload := append([]byte{t.natType}, t.noiseMgr.GetStaticPub()...)
	handshakePacket := protocol.NewGamePacket([4]byte{}, [4]byte{}, protocol.TypeHandshake, payload)
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
	err = handshakePacket.ParsePacket(t.bufPool, content)
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
		t.controlRecv(ctx)
	}()
	go func() {
		defer wg.Done()
		t.controlSend(ctx)
		cancel()
	}()
	go func() {
		defer wg.Done()
		t.heartbeat(ctx)
	}()
	go func() {
		defer wg.Done()
		//log.Printf("已启用 %d 个接收者\n", config.RecvWorkers)
		for i := 0; i < config.RecvWorkers; i++ {
			go t.packetRecv(ctx)
		}
		select {
		case <-ctx.Done():
		}

	}()
	go func() {
		defer wg.Done()
		//log.Printf("已启用 %d 个发送者\n", config.SendWorkers)
		for i := 0; i < config.SendWorkers; i++ {
			go t.packetSend(ctx)
		}
		select {
		case <-ctx.Done():
		}

	}()
	go func() {
		defer wg.Done()
		t.device.Start(ctx, protocol.HeaderLength)
	}()
	if testFlag {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t.testBroadCast(ctx, *testSecond)
		}()
	}
	if config.EnableP2P {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t.handleP2P(ctx)
		}()
	}
	if config.EnableUi {
		go func() {
			select {
			case <-ctx.Done():
				atomic.AddUint32(t.status, 1)
			}
		}()
	}

	atomic.AddUint32(t.status, 1)
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
		case <-ctx.Done():
			return
		}
	}
}

// controlRecv 接收控制消息
func (t *ClientTransport) controlRecv(ctx context.Context) {
	defer t.controlConn.Close()
	gp := &protocol.GamePacket{}
	readBuffer := bytes.NewBuffer(make([]byte, 512))
	for ctx.Err() == nil {
		//gp := &protocol.GamePacket{}
		t.controlConn.SetReadDeadline(time.Now().Add(time.Second * config.ReadTimeout))
		t.controlConn.SetPongHandler(func(string) error {
			//log.Println("pong")
			t.controlConn.SetReadDeadline(time.Now().Add(time.Second * config.ReadTimeout))
			return nil
		})
		msgType, reader, err := t.controlConn.NextReader()
		if err != nil {
			log.Println(err)
			break
		}
		if msgType == websocket.BinaryMessage {
			readBuffer.Reset()
			_, err = readBuffer.ReadFrom(reader)
			if err != nil {
				log.Println("Read buffer error:", err)
				continue
			}
			err = gp.ParseControl(readBuffer.Bytes())
			if err != nil {
				log.Println(err)
				continue
			}
			switch gp.PType {
			case protocol.TypeNoiseResponse:
				targetVip := gp.SourceVirtualIp()
				remotePub := gp.Payload[:32]
				t.initiateNoise(util.IpToKey(targetVip), remotePub)
			case protocol.TypeP2PCheck:
				log.Println("收到 check")
				t.checkP2P(gp.SourceVirtualIp())
			case protocol.TypeP2PCommand:
				t.addP2P([4]byte(gp.SourceVirtualIp().To4()), util.BytesToIP(gp.Payload[:6]), gp.Payload[6])
			default:
				continue
			}
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
	gp := &protocol.GamePacket{}
	headerData := make([]byte, 0, protocol.HeaderLength)
	var err error
	var encrypted []byte
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
			snapshot := t.p2pRouter.Load().(map[[4]byte]*stun.P2PStatus)
			for _, p := range payloadBatch {
				headerData = headerData[:0]
				dstIp := [4]byte(p[16:20])
				if dstIp[3] != 255 {
					session, ok := t.noiseMgr.GetSession(dstIp)
					if !ok {
						query := protocol.NewGamePacket(util.IpToKey(t.localIp), dstIp, protocol.TypeNoiseHandshake, nil)
						t.ControlSendChan <- query
						continue
					}
					if !session.IsReady() {
						continue
					}
					gp.Reset([4]byte(t.localIp.To4()), dstIp, protocol.TypeData, nil)
					gp.Length = uint16(len(p) + 16)
					headerData = gp.EncodeHeader(headerData)
					encrypted, err = session.Encrypt(p, headerData, t.bufPool)
					if err != nil {
						log.Println(err)
						continue
					}
					gp.Reset([4]byte(t.localIp.To4()), dstIp, protocol.TypeData, encrypted)
				} else {
					gp.Reset([4]byte(t.localIp.To4()), dstIp, protocol.TypeData, p)
				}

				data := gp.EncodePacket(t.bufPool)
				status, ok := snapshot[dstIp]
				if !ok {
					_, err = t.dataConn.WriteToUDP(data, t.serverAddr)
				} else if status.Established.Load() {
					_, err = t.dataConn.WriteToUDP(data, status.DstAddr)
				} else {
					_, err = t.dataConn.WriteToUDP(data, t.serverAddr)
				}
				t.bufPool.Put(encrypted[:0])
				t.bufPool.Put(data[:0])
				if err != nil {
					log.Println(err)
					continue
				}
			}
			atomic.AddUint64(t.sendPacketCount, uint64(len(payloadBatch)))
			payloadBatch = payloadBatch[:0]
		case <-ctx.Done():
			return
		}
	}
}

// packetRecv 接收并解包
func (t *ClientTransport) packetRecv(ctx context.Context) {
	buffer := make([]byte, 4*1024*1024)
	pongGp := protocol.NewGamePacket([4]byte(t.localIp.To4()), [4]byte{}, protocol.TypePong, nil)
	gpBuf := make([]byte, 0, 1024)
	headerData := make([]byte, 0, protocol.HeaderLength)
	pongBytes := pongGp.EncodePacketWithBuffer(gpBuf)
	for ctx.Err() == nil {
		headerData = headerData[:0]
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
		{
			atomic.AddUint64(t.recvPacketCount, 1)
			gp := &protocol.GamePacket{}
			err = gp.ParsePacket(t.bufPool, buffer[:cnt])
			if err != nil {
				log.Println(err)
				continue
			}
			packetEnd := protocol.HeaderLength + gp.Length
			if cnt < int(packetEnd) {
				log.Println(errorcode.PayloadMismatch)
				t.bufPool.Put(gp.RawData[:0])
				continue
			}
			switch gp.PType {
			case protocol.TypeData:
				srcVip := util.IpToKey(gp.SourceVirtualIp())
				headerData = gp.EncodeHeader(headerData)
				plain, response, err := t.noiseMgr.HandleNoisePacket(srcVip, headerData, gp.Payload)
				if err != nil {
					continue
				}
				if response != nil {
					respGp := protocol.NewGamePacket(util.IpToKey(t.localIp), srcVip, protocol.TypeData, response)
					data := respGp.EncodePacket(t.bufPool)
					t.dataConn.WriteToUDP(data, addr)
					t.bufPool.Put(data[:0])
				}
				plainLen := len(plain)
				if plainLen > 0 {
					gp.Payload = gp.Payload[:len(plain)]
				} else {
					t.bufPool.Put(gp.RawData[:0])
					continue
				}
				select {
				case t.FromNet <- gp.RawData:
				case <-ctx.Done():
					return
				default:
					t.bufPool.Put(gp.RawData[:0])
				}
			case protocol.TypePing:
				snapshot := t.p2pRouter.Load().(map[[4]byte]*stun.P2PStatus)

				srcVIp := util.IpToKey(gp.SourceVirtualIp())
				status, ok := snapshot[srcVIp]
				if ok {
					status.DstAddr = addr
				}
				_, err = t.dataConn.WriteToUDP(pongBytes, addr)
				t.bufPool.Put(gp.RawData[:0])
				if err != nil {
					log.Println(err)
					continue
				}
			case protocol.TypePong:
				snapshot := t.p2pRouter.Load().(map[[4]byte]*stun.P2PStatus)
				srcVIp := util.IpToKey(gp.SourceVirtualIp())
				status, ok := snapshot[srcVIp]
				if !ok {
					continue
				}
				status.DstAddr = addr
				status.UpdateLastSeen(true)
				t.bufPool.Put(gp.RawData[:0])
				controlGp := protocol.NewGamePacket(util.IpToKey(t.localIp), srcVIp, protocol.TypeP2PEstablished, nil)
				select {
				case t.ControlSendChan <- controlGp:
				case <-ctx.Done():
					return
				default:
					continue
				}
			}

		}
	}
}

// initiateNoise 当收到服务端返回的对端公钥时触发
func (t *ClientTransport) initiateNoise(remoteVip [4]byte, remotePub []byte) {
	t.routerMtx.Lock()
	defer t.routerMtx.Unlock()
	// TODO 暂存第一个数据包
	handshakeData, err := t.noiseMgr.GetHandshakeInit(remoteVip, remotePub, nil)
	if err != nil {
		log.Printf("生成 Noise Init 失败: %v", err)
		return
	}
	gp := protocol.NewGamePacket(util.IpToKey(t.localIp), remoteVip, protocol.TypeData, handshakeData)

	data := gp.EncodePacket(t.bufPool)
	_, err = t.dataConn.WriteToUDP(data, t.serverAddr)
	t.bufPool.Put(data[:0])

	log.Printf("已向 %v 发送 Noise 握手请求", remoteVip)
}

// P2P
func (t *ClientTransport) handleP2P(ctx context.Context) {
	timer := time.NewTicker(time.Second * 3)
	pingBuf := make([]byte, 0, 1024)
	pingData := protocol.NewGamePacket([4]byte(t.localIp.To4()), [4]byte{}, protocol.TypePing, nil).EncodePacketWithBuffer(pingBuf)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			snapshot := t.p2pRouter.Load().(map[[4]byte]*stun.P2PStatus)
			for vIp, status := range snapshot {
				if status.IsTimeout(10) {
					t.removeP2P(vIp)
					continue
				}
				if status.Established.Load() {
					_, err := t.dataConn.WriteToUDP(pingData, status.DstAddr)
					if err != nil {
						log.Println(err)
						continue
					}
				} else {
					err := t.punch(pingData, status)
					if err != nil {
						log.Println(err)
						continue
					}
				}
			}
		case <-ctx.Done():
			return
		}
	}
}

// 优化对称NAT的预测
func (t *ClientTransport) punch(data []byte, status *stun.P2PStatus) error {
	switch status.DstNatType {
	case stun.TypeCone:
		_, err := t.dataConn.WriteToUDP(data, status.DstAddr)
		if err != nil {
			return err
		}
	case stun.TypeSymmetric:
		dstIp := status.DstAddr.IP
		dstPort := status.DstAddr.Port
		for i := -100; i < 200; i++ {
			_, err := t.dataConn.WriteToUDP(data, &net.UDPAddr{IP: dstIp, Port: dstPort + i})
			if err != nil {
				return err
			}
			time.Sleep(time.Millisecond * 10)
		}
	default:
		log.Println("暂不支持的NAT类型: ", status.DstNatType)
	}
	return nil
}

func (t *ClientTransport) checkP2P(dst net.IP) {
	localAddr, _ := net.ResolveUDPAddr("udp", ":0")
	conn, err := net.ListenUDP("udp", localAddr)
	if err != nil {
		log.Println(err)
		return
	}
	defer conn.Close()
	checkGp := protocol.NewGamePacket(util.IpToKey(t.localIp.To4()), util.IpToKey(dst), protocol.TypeP2PCheck, nil)
	checkBytes := checkGp.EncodePacket(t.bufPool)
	_, err = conn.WriteTo(checkBytes, t.checkAddr)
	t.bufPool.Put(checkBytes[:0])
	if err != nil {
		log.Println(err)
		return
	}
}

func (t *ClientTransport) addP2P(vIp [4]byte, addr *net.UDPAddr, natType uint8) {
	t.routerMtx.Lock()
	defer t.routerMtx.Unlock()
	log.Println("尝试与 ", vIp, " 建立 p2p 连接")
	oldSnapshot := t.p2pRouter.Load().(map[[4]byte]*stun.P2PStatus)
	newP2P := &stun.P2PStatus{DstAddr: addr, LastSeen: time.Now().Unix(), DstNatType: natType}
	newMap := make(map[[4]byte]*stun.P2PStatus, len(oldSnapshot)+1)
	for k, v := range oldSnapshot {
		newMap[k] = v
	}
	newMap[vIp] = newP2P
	t.p2pRouter.Store(newMap)
}

func (t *ClientTransport) removeP2P(vIp [4]byte) {
	t.routerMtx.Lock()
	defer t.routerMtx.Unlock()
	log.Println("断开与 ", vIp, " p2p 连接")
	oldSnapshot := t.p2pRouter.Load().(map[[4]byte]*stun.P2PStatus)
	newMap := make(map[[4]byte]*stun.P2PStatus, len(oldSnapshot)-1)
	for k, v := range oldSnapshot {
		if k != vIp {
			newMap[k] = v
		}
	}
	t.p2pRouter.Store(newMap)
	controlGp := protocol.NewGamePacket(util.IpToKey(t.localIp), vIp, protocol.TypeP2PClosed, nil)
	select {
	case t.ControlSendChan <- controlGp:
	default:
		return
	}
}

func (t *ClientTransport) testBroadCast(ctx context.Context, second time.Duration) {
	timer := time.NewTicker(time.Millisecond * second)
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
			//log.Println("执行广播...")
			t.FromTun <- bch
		case <-ctx.Done():
			return
		}
	}
}
