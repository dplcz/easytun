package protocol

import (
	"crypto/cipher"
	"log"
	"sync/atomic"

	"golang.org/x/crypto/chacha20poly1305"
)

const (
	CipherInit = iota + 1
	CipherReady
)

type CipherState struct {
	aead  cipher.AEAD
	nonce uint64
}

type Cipher struct {
	Recver *CipherState
	Sender *CipherState
	status uint32
}

func NewChaCha() *Cipher {
	return &Cipher{
		status: CipherInit,
	}
}

func (c *Cipher) Init(recvKey, sendKey [32]byte) {
	aeadRecv, err := chacha20poly1305.New(recvKey[:])
	if err != nil {
		log.Fatal(err)
	}
	aeadSend, err := chacha20poly1305.New(sendKey[:])
	if err != nil {
		log.Fatal(err)
	}
	c.Recver = &CipherState{
		aead:  aeadRecv,
		nonce: 0,
	}
	c.Sender = &CipherState{
		aead:  aeadSend,
		nonce: 0,
	}
	c.ready()
}

func (c *Cipher) IsReady() bool {
	curStatus := atomic.LoadUint32(&c.status)
	if curStatus == CipherReady {
		return true
	}
	return false
}

func (c *Cipher) ready() {
	atomic.StoreUint32(&c.status, CipherReady)
}

func (cs *CipherState) GetNonce() uint64 {
	val := atomic.LoadUint64(&cs.nonce)
	atomic.AddUint64(&cs.nonce, 1)
	return val
}

func (cs *CipherState) Encrypt(dst, nonce, payload, header []byte) []byte {
	return cs.aead.Seal(dst, nonce, payload, header)
}

func (cs *CipherState) Decrypt(dst, nonce, payload, header []byte) ([]byte, error) {
	return cs.aead.Open(dst, nonce, payload, header)
}
