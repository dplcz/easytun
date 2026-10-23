package stun

import (
	"log"
	"net"
	"sync/atomic"
	"time"
)

type P2PStatus struct {
	DstNatType  uint8
	dstAddr     atomic.Pointer[net.UDPAddr]
	LastSeen    int64 // 最后一次收到对方 P2P 数据包的时间
	Established atomic.Bool
}

func (s *P2PStatus) SetDstAddr(addr *net.UDPAddr) {
	s.dstAddr.Store(addr)
}

func (s *P2PStatus) GetDstAddr() *net.UDPAddr {
	return s.dstAddr.Load()
}

func (s *P2PStatus) UpdateLastSeen(established bool) {
	atomic.StoreInt64(&s.LastSeen, time.Now().Unix())
	if s.Established.Load() == false && established {
		log.Println("建立 p2p 连接成功...")
		if addr := s.GetDstAddr(); addr != nil {
			log.Println(addr.String())
		}
	}
	s.Established.Store(established)
}

func (s *P2PStatus) IsTimeout(timeout int64) bool {
	last := atomic.LoadInt64(&s.LastSeen)
	return time.Now().Unix()-last > timeout
}
