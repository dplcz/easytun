package protocol

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/flynn/noise"
)

type NoiseSession struct {
	remoteVip   [4]byte
	Cipher      *Cipher
	hs          *noise.HandshakeState
	isInitiator bool
	lastSeen    int64
	mtx         sync.Mutex
}

func NewNoiseSession(remoteVip [4]byte, isInitiator bool, hs *noise.HandshakeState) *NoiseSession {
	return &NoiseSession{
		remoteVip:   remoteVip,
		isInitiator: isInitiator,
		lastSeen:    time.Now().Unix(),
		Cipher:      NewChaCha(),
		hs:          hs,
	}
}

type NoiseManager struct {
	myVip       [4]byte
	staticPriv  []byte
	staticPub   []byte
	cipherSuite noise.CipherSuite
	sessions    atomic.Value
	mtx         sync.Mutex
}

func NewNoiseManager() *NoiseManager {
	cs := noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashSHA256)
	keypair, _ := cs.GenerateKeypair(rand.Reader)
	log.Println("public key :", hex.EncodeToString(keypair.Public))

	noiseMgr := &NoiseManager{
		staticPriv:  keypair.Private,
		staticPub:   keypair.Public,
		cipherSuite: cs,
	}
	sessions := make(map[[4]byte]*NoiseSession)
	noiseMgr.sessions.Store(sessions)
	return noiseMgr
}

func (s *NoiseSession) updateLastSeen() {
	atomic.StoreInt64(&s.lastSeen, time.Now().Unix())
}

func (s *NoiseSession) isTimeout(timeout int64) bool {
	last := atomic.LoadInt64(&s.lastSeen)
	return time.Now().Unix()-last > timeout
}

func (m *NoiseManager) SetVirtualIp(vip [4]byte) {
	m.myVip = vip
}

func (m *NoiseManager) GetPublicKey() []byte {
	return m.staticPub
}

func (m *NoiseManager) GetSession(key [4]byte) (*NoiseSession, bool) {
	sessions := m.sessions.Load().(map[[4]byte]*NoiseSession)
	session, ok := sessions[key]
	if !ok {
		return nil, false
	}
	session.updateLastSeen()
	return session, true
}

func (m *NoiseManager) SetSession(key [4]byte, value *NoiseSession) {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	oldSessions := m.sessions.Load().(map[[4]byte]*NoiseSession)
	newSessions := make(map[[4]byte]*NoiseSession)
	for k, v := range oldSessions {
		newSessions[k] = v
	}
	newSessions[key] = value
	m.sessions.Store(newSessions)
}

func (m *NoiseManager) DeleteSession(key [4]byte) {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	oldSessions := m.sessions.Load().(map[[4]byte]*NoiseSession)
	newSessions := make(map[[4]byte]*NoiseSession)
	for k, v := range oldSessions {
		if k == key {
			continue
		}
		newSessions[k] = v
	}
	m.sessions.Store(newSessions)
}

func (m *NoiseManager) HandshakeInit(remoteVip [4]byte, remotePub []byte) ([]byte, error) {
	hs, _ := noise.NewHandshakeState(noise.Config{
		CipherSuite:   m.cipherSuite,
		Random:        rand.Reader,
		Pattern:       noise.HandshakeIK,
		Initiator:     true,
		StaticKeypair: noise.DHKey{Private: m.staticPriv, Public: m.staticPub},
		PeerStatic:    remotePub,
	})

	session := NewNoiseSession(remoteVip, true, hs)
	m.SetSession(remoteVip, session)
	msg, _, _, err := hs.WriteMessage(nil, nil)
	return msg, err
}

// HandleNoisePacket 供接收方调用，处理握手和数据解密
func (m *NoiseManager) HandleNoisePacket(srcVip [4]byte, data []byte) (*NoiseSession, []byte, error) {
	session, ok := m.GetSession(srcVip)

	if !ok {
		return m.handleResponderFirst(srcVip, data)
	} else if session.Cipher.IsReady() {
		return session, nil, nil
	}

	session.mtx.Lock()
	defer session.mtx.Unlock()
	// Double Check
	if session.Cipher.IsReady() {
		return session, nil, nil
	}
	// 碰撞检测
	if !session.Cipher.IsReady() && session.isInitiator && len(data) >= 64 {
		// IP 大的退让为 Responder
		if bytes.Compare(m.myVip[:], srcVip[:]) > 0 {
			return m.handleResponderFirst(srcVip, data)
		}
		return nil, nil, errors.New("noise: collision, wait for peer response")
	}

	// 接收握手响应 (Initiator 收到 Response)
	if session.isInitiator {
		_, csRecv, csSend, err := session.hs.ReadMessage(nil, data)
		if err != nil {
			return nil, nil, err
		}
		session.Cipher.Init(csRecv.UnsafeKey(), csSend.UnsafeKey())
		session.hs = nil
		return nil, nil, nil
	}
	return nil, nil, errors.New("noise: invalid state")
}

func (m *NoiseManager) handleResponderFirst(srcVip [4]byte, data []byte) (*NoiseSession, []byte, error) {
	hs, _ := noise.NewHandshakeState(noise.Config{
		CipherSuite:   m.cipherSuite,
		Random:        rand.Reader,
		Pattern:       noise.HandshakeIK,
		Initiator:     false,
		StaticKeypair: noise.DHKey{Private: m.staticPriv, Public: m.staticPub},
	})

	_, _, _, err := hs.ReadMessage(data[:0], data)
	if err != nil {
		return nil, nil, err
	}

	// 生成 Response 回发
	responseMsg, csSend, csRecv, _ := hs.WriteMessage(nil, nil)
	session := NewNoiseSession(srcVip, false, nil)
	session.Cipher.Init(csRecv.UnsafeKey(), csSend.UnsafeKey())
	m.SetSession(srcVip, session)

	return session, responseMsg, nil
}

func (m *NoiseManager) sweepSessions(timeout int64) {
	m.mtx.Lock()
	defer m.mtx.Unlock()

	oldSessions := m.sessions.Load().(map[[4]byte]*NoiseSession)
	newSessions := make(map[[4]byte]*NoiseSession)
	changed := false

	for k, v := range oldSessions {
		if v.isTimeout(timeout) {
			changed = true
		} else {
			newSessions[k] = v
		}
	}

	if changed {
		m.sessions.Store(newSessions)
	}
}

func (m *NoiseManager) CheckSession(ctx context.Context) {
	// 建议 ticker 时间不要长于 timeout 时间，否则清理会延迟很久
	ticker := time.NewTicker(time.Second * 10)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			m.sweepSessions(30)
		case <-ctx.Done():
			return
		}
	}
}
