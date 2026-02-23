package protocol

import (
	"bytes"
	"crypto/cipher"
	"crypto/rand"
	"easytun/internal/config"
	"easytun/internal/errorcode"
	"encoding/binary"
	"golang.org/x/crypto/chacha20poly1305"
	"net"
	"sync"
	"sync/atomic"
)

const (
	TypeHandshake = iota + 1
	TypePing
	TypePong
	TypeData
)

const MagicNumber = 0xDAAA

const HeaderLength = 9 + chacha20poly1305.NonceSize
const NonceLength = chacha20poly1305.NonceSize

var aead cipher.AEAD
var globalNonce uint64
var nonceBufPool = sync.Pool{New: func() interface{} { return make([]byte, NonceLength) }}

func InitChaCha() {
	var err error
	aead, err = chacha20poly1305.New(config.ClientKey)
	if err != nil {
		panic(err)
	}
	var b [8]byte
	_, err = rand.Read(b[:])
	if err != nil {
		panic(err)
	}
	globalNonce = binary.BigEndian.Uint64(b[:])
}

type GamePacket struct {
	src     [4]byte
	dst     [4]byte
	PType   uint8
	Payload []byte
	RawData []byte
	Length  uint16
}

func NewGamePacket(src, dst [4]byte, pType uint8, payload []byte) *GamePacket {
	if len(payload) == 0 {
		return &GamePacket{
			src:   src,
			dst:   dst,
			PType: pType,
		}
	}
	return &GamePacket{
		src:     src,
		dst:     dst,
		PType:   pType,
		Length:  uint16(len(payload)) + 16,
		Payload: payload,
	}
}

func (g *GamePacket) CalculateMs() int {
	//TODO implement me
	panic("implement me")
}

// [magic 2][pType 1][dst 4][length 2][nonce 12(src 4 + label 8)][payload]

func (g *GamePacket) encode(b []byte) []byte {
	nonceVal := atomic.AddUint64(&globalNonce, 1)
	nonceBuf := nonceBufPool.Get().([]byte)
	nonceBuf = nonceBuf[:12]
	copy(nonceBuf[0:4], g.src[:])
	binary.BigEndian.PutUint64(nonceBuf[4:], nonceVal)

	b = binary.BigEndian.AppendUint16(b, uint16(MagicNumber))
	b = append(b, g.PType)
	b = append(b, g.dst[:]...)
	b = binary.BigEndian.AppendUint16(b, g.Length)
	b = append(b, nonceBuf...)
	if g.Length > 0 {
		header := b[:HeaderLength-NonceLength]
		b = aead.Seal(b, nonceBuf, g.Payload, header)
	}
	nonceBufPool.Put(nonceBuf)
	return b
}

func (g *GamePacket) parse(data []byte, parsePayload bool) error {
	var magic uint16

	if len(data) < HeaderLength {
		return errorcode.PacketTooShort
	}
	reader := bytes.NewReader(data)
	nonceBuf := nonceBufPool.Get().([]byte)
	nonceBuf = nonceBuf[:12]
	if err := binary.Read(reader, binary.BigEndian, &magic); err != nil {
		return err
	}
	if magic != MagicNumber {
		return errorcode.InvalidMagic
	}
	if err := binary.Read(reader, binary.BigEndian, &g.PType); err != nil {
		return err
	}
	if err := binary.Read(reader, binary.BigEndian, &g.dst); err != nil {
		return err
	}
	if err := binary.Read(reader, binary.BigEndian, &g.Length); err != nil {
		return err
	}
	if err := binary.Read(reader, binary.BigEndian, &nonceBuf); err != nil {
		return err
	}

	g.src = [4]byte{nonceBuf[0], nonceBuf[1], nonceBuf[2], nonceBuf[3]}
	if aead != nil {
		if parsePayload && g.Length > 0 {
			header := data[:HeaderLength-NonceLength]
			ciphertext := data[HeaderLength:]
			decrypted, err := aead.Open(ciphertext[:0], nonceBuf, ciphertext, header)
			if err != nil {
				return err
			}
			g.Payload = decrypted
			g.RawData = data
		}
	} else {
		g.RawData = data
	}

	return nil
}

func (g *GamePacket) Destination() net.IP {
	return net.IPv4(g.dst[0], g.dst[1], g.dst[2], g.dst[3])
}

func (g *GamePacket) SourceVirtualIp() net.IP {
	return net.IPv4(g.src[0], g.src[1], g.src[2], g.src[3])
}

func (g *GamePacket) EncodePacket(pool *sync.Pool) []byte {
	dataLength := int(g.Length + HeaderLength)
	data := pool.Get().([]byte)
	if cap(data) < dataLength {
		pool.Put(data)
		data = make([]byte, dataLength)
	}
	data = data[:0]
	data = g.encode(data)
	return data
}

func (g *GamePacket) ParseHeader(data []byte) error {
	return g.parse(data, false)
}

func (g *GamePacket) ParsePacket(pool *sync.Pool, content []byte, parsePayload bool) error {
	data := pool.Get().([]byte)
	if cap(data) < len(content) {
		pool.Put(data[:0])
		data = make([]byte, len(content))
	}
	data = data[:len(content)]
	copy(data, content)
	return g.parse(data, parsePayload)
}
