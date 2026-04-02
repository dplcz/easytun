//go:build server

package transport

import (
	"context"
	"easytun/internal/protocol"
	"easytun/internal/stun"
	"easytun/internal/util"
	"log"
	"net/netip"
)

// ifEstablish 判断两个客户端之间是否可以尝试建立 P2P 隧道
func (h *Hub) ifEstablish(src, dst [4]byte, srcNat, dstNat uint8) bool {
	// 如果 NAT 类型未知，不进行打洞
	if srcNat <= stun.TypeUnknown || dstNat <= stun.TypeUnknown {
		return false
	}
	tunnel, ok := h.tm.Exist(src, dst)
	if !ok {
		// 第一次尝试，添加隧道记录
		h.tm.AddTunnel(src, dst, stun.TunnelInit)
		return true
	}
	// 如果之前失败过，且重试次数在限制内，则重新尝试
	if tunnel.GetStatus() == stun.TunnelFailed && tunnel.GetRetryTimes() < 6 {
		tunnel.ChangeStatus(stun.TunnelInit)
		return true
	}
	return false
}

// newP2PTask 创建一个新的 P2P 打洞任务并发送到调度队列
func (h *Hub) newP2PTask(ctx context.Context, srcVip, dstVip [4]byte, srcAddr, dstAddr netip.AddrPort) {
	newTask := stun.P2PTask{
		DstVip: dstVip,
		Dst:    dstAddr,
		SrcVip: srcVip,
		Src:    srcAddr,
	}
	select {
	case h.p2pTaskChan <- newTask:
		log.Println("创建 P2P 调度任务:", srcVip, "->", dstVip)
	case <-ctx.Done():
		return
	default:
		log.Println("P2P 调度队列已满，丢弃任务")
	}
}

// handleP2PTask 持续从任务队列中提取任务并下发打洞指令
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
			// 执行具体的指令下发逻辑
			h.handleP2PCommand(srcClient, dstClient, task)

		case <-ctx.Done():
			return
		}
	}
}

// handleP2PCommand 根据双方的 NAT 类型，通过控制通道下发不同的打洞/端口预测指令
func (h *Hub) handleP2PCommand(srcClient, dstClient *Client, task stun.P2PTask) {
	srcAddr, _ := srcClient.dataAddr.Load().(netip.AddrPort)
	dstAddr, _ := dstClient.dataAddr.Load().(netip.AddrPort)

	// 如果一方是对称 NAT (Symmetric NAT)，需要另一方协助进行端口刷新和 Check
	if srcClient.natType == stun.TypeSymmetric {
		// 通知 srcClient 刷新端口 (发送 Check 包到 Hub)
		srcClient.controlChan <- protocol.NewGamePacket(task.DstVip, task.SrcVip, protocol.TypeP2PCheck, nil)
		// 通知 dstClient 向 srcClient 可能的地址发送打洞包
		dstClient.controlChan <- protocol.NewGamePacket(task.SrcVip, task.DstVip, protocol.TypeP2PCommand, util.UDPAddrPortToBytes(srcAddr, srcClient.natType))
	}
	if dstClient.natType == stun.TypeSymmetric {
		dstClient.controlChan <- protocol.NewGamePacket(task.SrcVip, task.DstVip, protocol.TypeP2PCheck, nil)
		srcClient.controlChan <- protocol.NewGamePacket(task.DstVip, task.SrcVip, protocol.TypeP2PCommand, util.UDPAddrPortToBytes(dstAddr, dstClient.natType))
	}

	// 如果是对等圆锥型 NAT (Cone NAT)，直接下发对方的公网地址进行互打
	if dstClient.natType == stun.TypeCone {
		srcClient.controlChan <- protocol.NewGamePacket(task.DstVip, task.SrcVip, protocol.TypeP2PCommand, util.UDPAddrPortToBytes(dstAddr, dstClient.natType))
	}
	if srcClient.natType == stun.TypeCone {
		dstClient.controlChan <- protocol.NewGamePacket(task.SrcVip, task.DstVip, protocol.TypeP2PCommand, util.UDPAddrPortToBytes(srcAddr, srcClient.natType))
	}
}
