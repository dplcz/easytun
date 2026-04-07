package protocol

import (
	"easytun/internal/errorcode"
	"encoding/binary"
	"net"
	"sync"

	"golang.org/x/crypto/chacha20poly1305"
)

const (
	TypeHandshake = iota + 1
	TypeNoiseHandshake
	TypeNoiseResponse
	TypePing
	TypePong
	TypeData
	TypeP2PCheck
	TypeP2PCommand
	TypeP2PEstablished
	TypeP2PClosed
)

const FlagCompress = 0x80

const MagicNumber = 0xDAAA

const HeaderLength = 9 + chacha20poly1305.NonceSize
const NonceLength = chacha20poly1305.NonceSize

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

func (g *GamePacket) Reset(src, dst [4]byte, pType uint8, payload []byte) {
	g.src = src
	g.dst = dst
	g.PType = pType
	g.Payload = payload
	if len(payload) > 0 {
		g.Length = uint16(len(payload)) + 16
	} else {
		g.Length = 0
	}
}

func (g *GamePacket) CalculateMs() int {
	//TODO implement me
	panic("implement me")
}

// [magic 2][pType 1][dst 4][length 2][nonce 12(src 4 + label 8)][payload]

func (g *GamePacket) encode(b []byte, control bool, cipher *CipherState) []byte {
	var nonceVal uint64
	if cipher != nil {
		nonceVal = cipher.GetNonce()
	} else {
		nonceVal = 0
	}
	b = binary.BigEndian.AppendUint16(b, uint16(MagicNumber))
	b = append(b, g.PType)
	b = append(b, g.dst[:]...)
	b = binary.BigEndian.AppendUint16(b, g.Length)
	b = append(b, g.src[:]...)
	b = binary.BigEndian.AppendUint64(b, nonceVal)
	nonceBuf := b[HeaderLength-NonceLength : HeaderLength]
	if g.Length > 0 {
		header := b[:HeaderLength-NonceLength]
		if !control && cipher != nil {
			b = cipher.Encrypt(b, nonceBuf, g.Payload, header)
		} else {
			b = append(b, g.Payload...)
		}
	}
	return b
}

func (g *GamePacket) parse(data []byte, parsePayload bool, cipher *CipherState) error {
	if len(data) < HeaderLength {
		return errorcode.PacketTooShort
	}
	magic := binary.BigEndian.Uint16(data[0:2])
	if magic != MagicNumber {
		return errorcode.InvalidMagic
	}
	g.PType = data[2]
	g.dst = [4]byte(data[3:7])
	g.Length = binary.BigEndian.Uint16(data[7:9])
	nonceBuf := data[9 : 9+NonceLength]

	g.src = [4]byte(nonceBuf)
	if cipher != nil {
		if parsePayload && g.Length > 0 {
			header := data[:HeaderLength-NonceLength]
			ciphertext := data[HeaderLength:]
			decrypted, err := cipher.Decrypt(ciphertext[:0], nonceBuf, ciphertext, header)
			if err != nil {
				return err
			}
			g.Payload = decrypted
		}
	} else {
		g.Payload = data[HeaderLength:]
	}
	return nil
}

func (g *GamePacket) DecryptParse(pool *sync.Pool, cipher *CipherState) error {
	if cipher != nil {
		if g.Length > 0 {
			nonceBuf := g.RawData[9 : 9+NonceLength]
			header := g.RawData[:HeaderLength-NonceLength]
			ciphertext := g.RawData[HeaderLength:]
			decrypted, err := cipher.Decrypt(ciphertext[:0], nonceBuf, ciphertext, header)
			if err != nil {
				return err
			}
			g.Payload = decrypted
		}
	}
	if (g.PType & FlagCompress) != 0 {
		return g.decompress(pool)
	}
	return nil
}

func (g *GamePacket) Destination() net.IP {
	return net.IPv4(g.dst[0], g.dst[1], g.dst[2], g.dst[3])
}

func (g *GamePacket) SourceVirtualIp() net.IP {
	return net.IPv4(g.src[0], g.src[1], g.src[2], g.src[3])
}

func (g *GamePacket) EncodePacket(pool *sync.Pool, control, compress bool, cipher *CipherState) []byte {
	if compress {
		if g.compress(pool) {
			g.PType |= FlagCompress
		}
	}
	dataLength := int(g.Length + HeaderLength)
	data := pool.Get().([]byte)
	if cap(data) < dataLength {
		pool.Put(data)
		data = make([]byte, dataLength)
	}
	data = data[:0]
	data = g.encode(data, control, cipher)
	return data
}

func (g *GamePacket) EncodePacketWithBuffer(data []byte, control bool, cipher *CipherState) []byte {
	data = g.encode(data, control, cipher)
	return data
}

func (g *GamePacket) ParsePacket(pool *sync.Pool, content []byte, parsePayload bool, cipher *CipherState) error {
	data := pool.Get().([]byte)
	if cap(data) < len(content) {
		pool.Put(data[:0])
		data = make([]byte, len(content))
	}
	data = data[:len(content)]
	copy(data, content)
	g.RawData = data
	return g.parse(data, parsePayload, cipher)
}

func (g *GamePacket) ParseControl(data []byte) error {
	if len(data) < HeaderLength {
		return errorcode.PacketTooShort
	}
	magic := binary.BigEndian.Uint16(data[0:2])
	if magic != MagicNumber {
		return errorcode.InvalidMagic
	}
	g.PType = data[2]
	g.dst = [4]byte(data[3:7])
	g.Length = binary.BigEndian.Uint16(data[7:9])
	nonceBuf := data[9 : 9+NonceLength]

	g.src = [4]byte(nonceBuf)
	g.Payload = data[9+NonceLength:]
	return nil
}
