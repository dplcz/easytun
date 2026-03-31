package crypt

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"sync"

	"github.com/flynn/noise"
)

// NoiseSession 存储与特定对端的加密状态
type NoiseSession struct {
	mtx         sync.Mutex
	remoteVip   [4]byte
	hs          *noise.HandshakeState
	csSend      *noise.CipherState
	csRecv      *noise.CipherState
	isInitiator bool
	isReady     bool
}

type NoiseManager struct {
	myVip       [4]byte
	staticPriv  []byte
	staticPub   []byte
	cipherSuite noise.CipherSuite

	sessions map[[4]byte]*NoiseSession
	mtx      sync.RWMutex
}

func NewNoiseManager() *NoiseManager {
	// 生产环境建议从配置文件读取或持久化此 Key
	cs := noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashSHA256)
	keypair, _ := cs.GenerateKeypair(rand.Reader)
	log.Println("public key :", hex.EncodeToString(keypair.Public))
	return &NoiseManager{
		staticPriv:  keypair.Private,
		staticPub:   keypair.Public,
		cipherSuite: cs,
		sessions:    make(map[[4]byte]*NoiseSession),
	}
}

func (m *NoiseManager) SetVirtualIp(vip [4]byte) {
	m.myVip = vip
}

// GetHandshakeInit 供发送方调用，生成第一个 0-RTT 包
func (m *NoiseManager) GetHandshakeInit(remoteVip [4]byte, remotePub []byte, firstPayload []byte) ([]byte, error) {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	hs, _ := noise.NewHandshakeState(noise.Config{
		CipherSuite:   m.cipherSuite,
		Random:        rand.Reader,
		Pattern:       noise.HandshakeIK,
		Initiator:     true,
		StaticKeypair: noise.DHKey{Private: m.staticPriv, Public: m.staticPub},
		PeerStatic:    remotePub,
	})

	session := &NoiseSession{
		remoteVip:   remoteVip,
		hs:          hs,
		isInitiator: true,
	}
	m.sessions[remoteVip] = session

	// Noise IK 0-RTT: 第一个包即可带 Payload
	msg, _, _, err := hs.WriteMessage(nil, firstPayload)
	return msg, err
}

// HandleNoisePacket 供接收方调用，处理握手和数据解密
func (m *NoiseManager) HandleNoisePacket(srcVip [4]byte, header, data []byte) ([]byte, []byte, error) {
	m.mtx.Lock()
	session, ok := m.sessions[srcVip]
	m.mtx.Unlock()

	// 1. 全新握手 (Responder 角色)
	if !ok {
		return m.handleResponderFirst(srcVip, data)
	}

	session.mtx.Lock()
	defer session.mtx.Unlock()

	if session.isReady {
		// 这里可以使用 AD (比如 srcVip) 增强安全性
		plain, err := session.csRecv.Decrypt(data[:0], header, data)
		return plain, nil, err
	}

	// 碰撞检测
	if !session.isReady && session.isInitiator && len(data) >= 64 {
		// IP 大的退让为 Responder
		if bytes.Compare(m.myVip[:], srcVip[:]) > 0 {
			return m.handleResponderFirst(srcVip, data)
		}
		return nil, nil, errors.New("noise: collision, wait for peer response")
	}

	// 接收握手响应 (Initiator 收到 Response)
	if !session.isReady && session.isInitiator {
		plain, csRecv, csSend, err := session.hs.ReadMessage(nil, data)
		if err != nil {
			return nil, nil, err
		}
		session.csSend = csSend
		session.csRecv = csRecv
		session.isReady = true
		session.hs = nil
		return plain, nil, nil
	}

	return nil, nil, errors.New("noise: invalid state")
}

func (m *NoiseManager) handleResponderFirst(srcVip [4]byte, data []byte) ([]byte, []byte, error) {
	hs, _ := noise.NewHandshakeState(noise.Config{
		CipherSuite:   m.cipherSuite,
		Random:        rand.Reader,
		Pattern:       noise.HandshakeIK,
		Initiator:     false,
		StaticKeypair: noise.DHKey{Private: m.staticPriv, Public: m.staticPub},
	})

	plain, _, _, err := hs.ReadMessage(nil, data)
	if err != nil {
		return nil, nil, err
	}

	// 生成 Response 回发
	responseMsg, csSend, csRecv, _ := hs.WriteMessage(nil, nil)
	session := &NoiseSession{
		remoteVip:   srcVip,
		csSend:      csSend,
		csRecv:      csRecv,
		isReady:     true, // Responder 收到包后即可认为 Ready
		isInitiator: false,
	}

	m.mtx.Lock()
	m.sessions[srcVip] = session
	m.mtx.Unlock()

	return plain, responseMsg, nil
}

func (m *NoiseManager) GetStaticPub() []byte {
	return m.staticPub
}
func (m *NoiseManager) GetSession(key [4]byte) (*NoiseSession, bool) {
	// TODO 改为原子快照
	m.mtx.Lock()
	defer m.mtx.Unlock()
	session, ok := m.sessions[key]
	return session, ok
}

func (s *NoiseSession) IsReady() bool {
	return s.isReady
}

func (s *NoiseSession) Encrypt(data, header []byte, pool *sync.Pool) ([]byte, error) {
	dataLength := len(data)
	if cap(data) < dataLength+16 {
		encrypted, err := s.csSend.Encrypt(nil, header, data)
		pool.Put(data[:0])
		return encrypted, err
	}
	encrypted, err := s.csSend.Encrypt(data[:0], header, data)
	return encrypted, err

}
