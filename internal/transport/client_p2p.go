//go:build client

package transport

import (
	"context"
	"easytun/internal/protocol"
	"easytun/internal/stun"
	"easytun/internal/util"
	"log"
	"net"
	"time"
)

// handleP2P 定时检查 P2P 路由表中的客户端状态，执行打洞或维护心跳逻辑
func (t *ClientTransport) handleP2P(ctx context.Context) error {
	timer := time.NewTicker(time.Second * 3)
	pingBuf := make([]byte, 0, 1024)
	pingData := protocol.NewGamePacket([4]byte(t.localIp.To4()), [4]byte{}, protocol.TypePing, nil).EncodePacketWithBuffer(pingBuf, true, nil)
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
						log.Println("P2P 心跳发送失败:", err)
						continue
					}
				} else {
					err := t.punch(pingData, status)
					if err != nil {
						log.Println("P2P 打洞尝试失败:", err)
						continue
					}
				}
			}
		case <-ctx.Done():
			return nil
		}
	}
}

// punch 根据目标的 NAT 类型执行对应的打洞策略，支持 Cone 和 Symmetric NAT
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

// checkP2P 向服务端的 Check 端口发送探测包，辅助对称 NAT 的端口预测
func (t *ClientTransport) checkP2P(dst net.IP) {
	localAddr, _ := net.ResolveUDPAddr("udp", ":0")
	conn, err := net.ListenUDP("udp", localAddr)
	if err != nil {
		log.Println(err)
		return
	}
	defer conn.Close()
	checkGp := protocol.NewGamePacket(util.IpToKey(t.localIp.To4()), util.IpToKey(dst), protocol.TypeP2PCheck, nil)
	checkBytes := checkGp.EncodePacket(t.bufPool, true, nil)
	_, err = conn.WriteTo(checkBytes, t.checkAddr)
	t.bufPool.Put(checkBytes[:0])
	if err != nil {
		log.Println(err)
		return
	}
}

// addP2P 向 P2P 路由表中添加或更新目标客户端的状态，开始打洞流程
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

// removeP2P 从 P2P 路由表中移除目标客户端，并通知服务端同步状态
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
