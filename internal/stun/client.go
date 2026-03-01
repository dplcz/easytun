package stun

import (
	"log"
	"net"
	"sync/atomic"
	"time"
)

type P2PStatus struct {
	DstNatType  uint8
	DstAddr     *net.UDPAddr
	LastSeen    int64 // 最后一次收到对方 P2P 数据包的时间
	Established atomic.Bool
	//isActive uint32    // 原子操作：0 为中转，1 为直连
	//probeCount int           // 连续丢失的心跳数
	//rTT        time.Duration // 链路延迟
}

func (s *P2PStatus) UpdateLastSeen(established bool) {
	atomic.StoreInt64(&s.LastSeen, time.Now().Unix())
	if s.Established.Load() == false && established {
		log.Println("建立 p2p 连接成功...")
		log.Println(s.DstAddr.String())
	}
	s.Established.Store(established)
}

func (s *P2PStatus) IsTimeout(timeout int64) bool {
	last := atomic.LoadInt64(&s.LastSeen)
	return time.Now().Unix()-last > timeout
}
