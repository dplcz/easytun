package stun

import (
	"net"
	"sync/atomic"
	"time"
)

const (
	TypeBlock = iota + 1
	TypeCone
	TypeSymmetric
	TypeUnknown
)

const (
	TunnelInit = iota + 1
	TunnelBuilding
	TunnelConnected
	TunnelFailed
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
	s.Established.Store(established)
}

func (s *P2PStatus) IsTimeout(timeout int64) bool {
	last := atomic.LoadInt64(&s.LastSeen)
	return time.Now().Unix()-last > timeout
}

type P2PTask struct {
	DstVip [4]byte
	Dst    *net.UDPAddr
	SrcVip [4]byte
	Src    *net.UDPAddr
}
type P2PTunnel struct {
	Status     uint32
	RetryTimes uint32
}

func NewP2PTunnel() *P2PTunnel {
	return &P2PTunnel{
		Status:     TunnelInit,
		RetryTimes: 0,
	}
}

func (t *P2PTunnel) AddRetryTimes() {
	atomic.AddUint32(&t.RetryTimes, 1)
}

func (t *P2PTunnel) GetRetryTimes() uint32 {
	return atomic.LoadUint32(&t.RetryTimes)
}

func (t *P2PTunnel) ChangeStatus(delta uint32) {
	atomic.AddUint32(&t.Status, delta)
}
func (t *P2PTunnel) GetStatus() uint32 {
	return atomic.LoadUint32(&t.Status)
}
