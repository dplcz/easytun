//go:build server

package transport

import (
	"context"
	"easytun/internal/config"
	"easytun/internal/errorcode"
	"easytun/internal/protocol"
	"easytun/internal/util"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"runtime"
	"time"

	"golang.org/x/net/ipv4"
)

// listenUdp 启动 UDP 监听，负责接收所有客户端发来的数据包并进行初步分发
func (h *Hub) listenUdp(ctx context.Context) {
	var rawPType uint8

	udpAddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf(":%d", config.ServerPort))
	if err != nil {
		log.Fatalf("UDP 地址解析失败: %v", err)
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		log.Fatalf("UDP 监听失败: %v", err)
	}
	// 设置较大的缓冲区以应对突发流量
	conn.SetReadBuffer(4 * 1024 * 1024)  // 4MB
	conn.SetWriteBuffer(4 * 1024 * 1024) // 4MB

	h.PacketConn = ipv4.NewPacketConn(conn)
	h.UdpConn = conn
	batchSize := 128
	msgs := make([]ipv4.Message, batchSize)

	// 根据 CPU 核心数启动处理协程
	workerCount := runtime.NumCPU() * 2

	// 启动中转处理协程
	for i := 0; i < 2; i++ {
		go h.transfer(ctx)
	}
	log.Println("启动 2 个处理协程")

	// 启动批量发送协程
	for i := 0; i < workerCount; i++ {
		go h.writeUdpPacket(ctx)
	}
	log.Printf("启动 %d 个发送协程\n", workerCount)
	log.Println("启动 p2p 调度协程")
	go h.handleP2PTask(ctx)
	go h.listenCheck(ctx)

	log.Println("开始监听UDP...")
	for i := range msgs {
		msgs[i].Buffers = [][]byte{make([]byte, 2048)}
	}
	for ctx.Err() == nil {
		h.UdpConn.SetReadDeadline(time.Now().Add(config.ReadTimeout * time.Second))
		// 使用 ReadBatch 提升读取性能
		count, err := h.PacketConn.ReadBatch(msgs, 0)
		if err != nil {
			var netErr *net.OpError
			if errors.As(err, &netErr) && netErr.Timeout() {
				continue
			}
			log.Println("UDP 读取错误:", err)
			continue
		}
		for i := 0; i < count; i++ {
			msg := msgs[i]
			cnt := msg.N                       // 实际字节数
			srcAddr := msg.Addr.(*net.UDPAddr) // 发送方公网地址
			srcAddrPort := srcAddr.AddrPort()  // 优化点：转换为 netip.AddrPort
			payload := msg.Buffers[0][:cnt]

			consume := false
			tp := h.tpPool.Get().(*transferPacket)
			tp.srcAddr = srcAddrPort
			gp := tp.gp
			err = gp.ParsePacket(h.bufPool, payload, true, nil)
			if err != nil {
				log.Println("UDP 数据包解析失败:", err)
				goto CleanUp
			}
			if cnt < int(gp.Length) {
				log.Println(errorcode.PayloadMismatch)
				goto CleanUp
			}

			rawPType = gp.PType & (^uint8(protocol.FlagCompress))
			switch rawPType {
			case protocol.TypePing:
				// 处理数据平面心跳，更新客户端地址
				snapshot := h.router.Load().(*routerSnapshot)
				srcVip := util.IpToKey(gp.SourceVirtualIp())
				if client, ok := snapshot.clientMap[srcVip]; ok {
					client.updateAddrCheck(srcAddrPort)
				}
				goto CleanUp
			case protocol.TypeData:
				// 数据包进入中转队列
			default:
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
				log.Println("transferChan已满，丢弃包")
				goto CleanUp
			}
		CleanUp:
			if !consume {
				h.bufPool.Put(tp.gp.RawData[:0])
				gp.RawData = nil
				h.tpPool.Put(tp)
			}
		}
	}
}

// listenCheck 监听 P2P Check 端口，辅助对称 NAT 客户端进行端口预测
func (h *Hub) listenCheck(ctx context.Context) {
	var srcClient, dstClient *Client
	var snapShot *routerSnapshot
	var srcOk, dstOk, ok bool
	var srcVip, dstVip [4]byte
	gp := &protocol.GamePacket{}
	checkAddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf(":%d", config.CheckPort))
	if err != nil {
		log.Fatalf("UDP check 地址解析失败: %v", err)
	}
	conn, err := net.ListenUDP("udp", checkAddr)
	if err != nil {
		log.Fatalf("UDP check 监听失败: %v", err)
	}
	conn.SetReadBuffer(4 * 1024 * 1024)
	conn.SetWriteBuffer(4 * 1024 * 1024)
	h.CheckUdpConn = conn
	buffer := make([]byte, 1024*2)
	for ctx.Err() == nil {
		h.CheckUdpConn.SetReadDeadline(time.Now().Add(config.ReadTimeout * time.Second))
		cnt, addr, err := h.CheckUdpConn.ReadFromUDP(buffer)
		if err != nil {
			var netErr *net.OpError
			if errors.As(err, &netErr) && netErr.Timeout() {
				continue
			}
			log.Println("Check 端口读取错误:", err)
			continue
		}
		addrPort := addr.AddrPort()
		err = gp.ParseControl(buffer[:cnt])
		if err != nil {
			log.Println("Check 数据包解析错误:", err)
			continue
		}
		switch gp.PType {
		case protocol.TypeP2PCheck:
			// 收到 Check 包后，通知目标客户端发起连接，协助打洞
			_, ok = h.tm.Exist(util.IpToKey(gp.SourceVirtualIp()), util.IpToKey(gp.Destination()))
			if ok {
				snapShot = h.router.Load().(*routerSnapshot)
				srcVip = util.IpToKey(gp.SourceVirtualIp())
				dstVip = util.IpToKey(gp.Destination())
				srcClient, srcOk = snapShot.clientMap[srcVip]
				dstClient, dstOk = snapShot.clientMap[dstVip]
				if srcOk && dstOk {
					log.Println("收到 P2P Check 信号:", addrPort)
					dstClient.controlChan <- protocol.NewGamePacket(srcVip, dstVip, protocol.TypeP2PCommand, util.UDPAddrPortToBytes(addrPort, srcClient.natType))
				}
			}
		default:
			continue
		}

	}
}

// writeUdpPacket 批量将数据包写回到 UDP 网络
func (h *Hub) writeUdpPacket(ctx context.Context) {
	batchSize := 16
	msgs := make([]ipv4.Message, 0, batchSize*4)
	packetBatch := make([]*packet, 0, batchSize)
	for {
		select {
		case pa := <-h.packetChan:
			packetBatch = append(packetBatch, pa)
			// 尽量积累一批数据包后再发送
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
					// 处理广播
					bBuf := [][]byte{p.data}
					for _, client := range snapshot.clientSlice {
						addrVal := client.dataAddr.Load()
						if addrVal == nil {
							continue
						}
						addr := addrVal.(netip.AddrPort)
						if !addr.IsValid() {
							continue
						}
						msgs = append(msgs, ipv4.Message{
							Buffers: bBuf,
							Addr:    net.UDPAddrFromAddrPort(addr),
						})
					}
				} else {
					// 单播发送
					if p.dstAddr.IsValid() {
						msgs = append(msgs, ipv4.Message{
							Buffers: [][]byte{p.data},
							Addr:    net.UDPAddrFromAddrPort(p.dstAddr),
						})
					}
				}
			}
			// 使用 WriteBatch 批量发送
			_, err := h.PacketConn.WriteBatch(msgs, 0)
			if err != nil {
				log.Println("批量发送 UDP 错误:", err)
			}
			// 清理并放回对象池
			for i, p := range packetBatch {
				h.bufPool.Put(p.data[:0])
				p.data = nil
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
