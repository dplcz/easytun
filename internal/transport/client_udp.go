//go:build client

package transport

import (
	"context"
	"easytun/internal/config"
	"easytun/internal/protocol"
	"easytun/internal/stun"
	"easytun/internal/util"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"net"
	"sync/atomic"
	"time"
)

// packetSend 从 TUN 读取原始 IP 包，根据路由表进行 Noise 加密或直接转发
func (t *ClientTransport) packetSend(ctx context.Context) error {
	batchSize := 32
	payloadBatch := make([][]byte, 0, batchSize)
	var err error
	dnsBuffer := make([]byte, 0, 64)
	gp := &protocol.GamePacket{}
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
				dstIp := [4]byte(p[16:20])
				var data []byte
				ihl := int(p[0]&0x0F) * 4
				isDNS := false
				if len(p) >= ihl+8 && ihl >= 20 && p[0]>>4 == 4 && p[9] == 17 && dstIp == util.IpToKey(t.device.Dns()) {
					udpHeader := p[ihl:]
					dstPort := binary.BigEndian.Uint16(udpHeader[2:4])
					isDNS = dstPort == 53
				}
				if isDNS {
					dnsBuffer = dnsBuffer[:0]
					udpHeader := p[ihl:]
					srcPort := binary.BigEndian.Uint16(udpHeader[0:2])
					dnsBuffer = binary.BigEndian.AppendUint16(dnsBuffer, srcPort)
					dnsBuffer = append(dnsBuffer, p[ihl+8:]...)
					gp.Reset([4]byte(t.localIp.To4()), dstIp, protocol.TypeDnsRequest, dnsBuffer)
					data = gp.EncodePacket(t.bufPool, false, false, nil)
				} else if dstIp[3] != 255 {
					session, ok := t.noiseMgr.GetSession(dstIp)
					if !ok {
						query := protocol.NewGamePacket(util.IpToKey(t.localIp), dstIp, protocol.TypeNoiseHandshake, p)
						select {
						case t.ControlSendChan <- query:
						case <-ctx.Done():
							return nil
						}
						continue
					}
					if !session.Cipher.IsReady() {
						session.AddPendingData(p)
						continue
					}
					gp.Reset([4]byte(t.localIp.To4()), dstIp, protocol.TypeData, p)
					session.UpdateLastSeen()
					data = gp.EncodePacket(t.bufPool, false, config.EnableCompress, session.Cipher.Sender)
				} else {
					gp.Reset([4]byte(t.localIp.To4()), dstIp, protocol.TypeData, p)
					data = gp.EncodePacket(t.bufPool, false, false, nil)
				}
				status, ok := snapshot[dstIp]
				var n int
				if !ok {
					n, err = t.dataConn.WriteToUDP(data, t.serverAddr)
				} else if status.Established.Load() {
					n, err = t.dataConn.WriteToUDP(data, status.DstAddr)
				} else {
					n, err = t.dataConn.WriteToUDP(data, t.serverAddr)
				}
				t.bufPool.Put(gp.Payload[:0])
				t.bufPool.Put(data[:0])
				if err != nil {
					log.Println("UDP 发送数据失败:", err)
					continue
				} else {
					atomic.AddUint64(t.sendBytes, uint64(n))
				}
			}
			atomic.AddUint64(t.sendPacketCount, uint64(len(payloadBatch)))
			payloadBatch = payloadBatch[:0]
		case <-ctx.Done():
			return nil
		}
	}
}

// packetRecv 从 UDP 接收数据包，处理 Noise 握手响应或解密数据包
func (t *ClientTransport) packetRecv(ctx context.Context) error {
	buffer := make([]byte, 4*1024*1024)
	pongGp := protocol.NewGamePacket([4]byte(t.localIp.To4()), [4]byte{}, protocol.TypePong, nil)
	gpBuf := make([]byte, 0, 1024)
	pongBytes := pongGp.EncodePacketWithBuffer(gpBuf, true, nil)
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
			t.dataConn.SetReadDeadline(time.Now().Add(config.ReadTimeout))
			cnt, addr, err := t.dataConn.ReadFromUDP(buffer)
			if err != nil {
				var netErr *net.OpError
				if errors.As(err, &netErr) && netErr.Timeout() {
					continue
				}
				return fmt.Errorf("UDP 读取错误: %w", err)
			}
			{
				atomic.AddUint64(t.recvPacketCount, 1)
				atomic.AddUint64(t.recvBytes, uint64(cnt))
				gp := &protocol.GamePacket{}
				err = gp.ParsePacket(t.bufPool, buffer[:cnt], true, nil)
				if err != nil {
					continue
				}
				switch gp.PType & (^uint8(protocol.FlagCompress)) {
				case protocol.TypeData:
					srcVip := util.IpToKey(gp.SourceVirtualIp())
					session, response, err := t.noiseMgr.HandleNoisePacket(srcVip, gp.Payload)
					if err != nil {
						log.Println(err)
						t.bufPool.Put(gp.RawData[:0])
						continue
					}
					if response != nil {
						respGp := protocol.NewGamePacket(util.IpToKey(t.localIp), srcVip, protocol.TypeData, response)
						data := respGp.EncodePacket(t.bufPool, false, false, nil)
						t.dataConn.WriteToUDP(data, addr)
						t.bufPool.Put(data[:0])
						t.bufPool.Put(gp.RawData[:0])
						continue
					}
					if session == nil {
						continue
					}
					err = gp.DecryptParse(t.bufPool, session.Cipher.Recver)
					if err != nil {
						log.Println(err)
						t.bufPool.Put(gp.RawData[:0])
						continue
					} else {
						session.UpdateLastSeen()
					}
					select {
					case t.FromNet <- gp.RawData:
					case <-ctx.Done():
						return nil
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
						t.bufPool.Put(gp.RawData[:0])
						continue
					}
					status.DstAddr = addr
					status.UpdateLastSeen(true)
					t.bufPool.Put(gp.RawData[:0])
					controlGp := protocol.NewGamePacket(util.IpToKey(t.localIp), srcVIp, protocol.TypeP2PEstablished, nil)
					select {
					case t.ControlSendChan <- controlGp:
					case <-ctx.Done():
						return nil
					default:
						continue
					}
				case protocol.TypeDnsResponse:
					dnsBuf, err := util.BuildDNSResponse(gp.Payload, t.localIp, t.device.Dns())
					if err != nil {
						log.Println(err)
						t.bufPool.Put(gp.RawData[:0])
					}
					err = t.device.SendDNS(dnsBuf)
					if err != nil {
						log.Println(err)
					}
					t.bufPool.Put(gp.RawData[:0])
				}
			}
		}
	}
}

func (t *ClientTransport) initiateNoise(remoteVip [4]byte, remotePub, firstPayload []byte) {
	handshakeData, err := t.noiseMgr.HandshakeInit(remoteVip, remotePub, firstPayload)
	if err != nil {
		log.Printf("生成 Noise Init 失败: %v", err)
		return
	}
	gp := protocol.NewGamePacket(util.IpToKey(t.localIp), remoteVip, protocol.TypeData, handshakeData)

	data := gp.EncodePacket(t.bufPool, true, false, nil)
	_, err = t.dataConn.WriteToUDP(data, t.serverAddr)
	t.bufPool.Put(data[:0])
}
