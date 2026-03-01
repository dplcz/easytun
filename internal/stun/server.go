//go:build server

package stun

import (
	"bytes"
	"net"
	"sync"
	"sync/atomic"
)

const (
	TunnelInit = iota + 1
	TunnelConnected
	TunnelFailed
)

type P2PTask struct {
	DstVip [4]byte
	Dst    *net.UDPAddr
	SrcVip [4]byte
	Src    *net.UDPAddr
}
type P2PTunnel struct {
	A          [4]byte
	B          [4]byte
	Status     uint32
	RetryTimes uint32
	InitCnt    uint8
}

func (t *P2PTunnel) AddRetryTimes() {
	atomic.AddUint32(&t.RetryTimes, 1)
}
func (t *P2PTunnel) GetRetryTimes() uint32 {
	return atomic.LoadUint32(&t.RetryTimes)
}
func (t *P2PTunnel) ChangeStatus(status uint32) {
	atomic.StoreUint32(&t.Status, status)
}
func (t *P2PTunnel) GetStatus() uint32 {
	return atomic.LoadUint32(&t.Status)
}

type TunnelManager struct {
	mtx     sync.RWMutex
	tunnels map[[8]byte]*P2PTunnel
}

func NewTunnelManager() *TunnelManager {
	return &TunnelManager{
		tunnels: make(map[[8]byte]*P2PTunnel),
	}
}

// makeKey 生成唯一的通道哈希键，保证 A-B 和 B-A 生成的 Key 一致
func makeKey(A, B [4]byte) [8]byte {
	var key [8]byte
	// 始终把较小的 IP 放在前面，消除方向性
	if bytes.Compare(A[:], B[:]) < 0 {
		copy(key[0:4], A[:])
		copy(key[4:8], B[:])
	} else {
		copy(key[0:4], B[:])
		copy(key[4:8], A[:])
	}
	return key
}

// Exist 查找通道，时间复杂度 O(1)
func (tm *TunnelManager) Exist(A, B [4]byte) (*P2PTunnel, bool) {
	tm.mtx.RLock()
	defer tm.mtx.RUnlock()

	key := makeKey(A, B)
	t, ok := tm.tunnels[key]
	return t, ok // 返回指针，外部调用 t.ChangeStatus() 会直接生效
}

// AddTunnel 添加通道
func (tm *TunnelManager) AddTunnel(A, B [4]byte, status uint32) {
	tm.mtx.Lock()
	defer tm.mtx.Unlock()

	key := makeKey(A, B)
	if _, exists := tm.tunnels[key]; !exists {
		tm.tunnels[key] = &P2PTunnel{
			A:      A,
			B:      B,
			Status: status,
		}
	}
}

// RemoveTunnel 移除指定的两端通道
func (tm *TunnelManager) RemoveTunnel(A, B [4]byte) {
	tm.mtx.Lock()
	defer tm.mtx.Unlock()

	key := makeKey(A, B)
	delete(tm.tunnels, key)
}

// RemoveA 移除包含某个 IP 的所有通道 (用于客户端掉线时清理)
func (tm *TunnelManager) RemoveA(A [4]byte) {
	tm.mtx.Lock()
	defer tm.mtx.Unlock()

	// map 的遍历删除在 Go 中是安全的
	for key, t := range tm.tunnels {
		if t.A == A || t.B == A {
			delete(tm.tunnels, key)
		}
	}
}
