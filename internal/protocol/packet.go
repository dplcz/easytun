package protocol

import (
	"easytun/internal/errorcode"
	"encoding/binary"
	"net"
	"sync"
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

const MagicNumber = 0xDAAA

const HeaderLength = 9

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
		Length:  uint16(len(payload)),
		Payload: payload,
	}
}

func (g *GamePacket) Reset(src, dst [4]byte, pType uint8, payload []byte) {
	g.src = src
	g.dst = dst
	g.PType = pType
	g.Payload = payload
	if len(payload) > 0 {
		g.Length = uint16(len(payload))
	} else {
		g.Length = 0
	}
}

func (g *GamePacket) CalculateMs() int {
	//TODO implement me
	panic("implement me")
}

// [magic 2][pType 1][dst 4][length 2][src 4][payload]

func (g *GamePacket) encode(b []byte, isHeader bool) []byte {
	b = binary.BigEndian.AppendUint16(b, uint16(MagicNumber))
	b = append(b, g.PType)
	b = append(b, g.dst[:]...)
	b = binary.BigEndian.AppendUint16(b, g.Length)
	b = append(b, g.src[:]...)
	if !isHeader && g.Length > 0 {
		b = append(b, g.Payload...)
	}
	return b
}

func (g *GamePacket) parse(data []byte) error {
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
	g.src = [4]byte(data[9:13])

	if g.Length > 0 {
		g.Payload = data[HeaderLength:]
	}

	return nil
}

func (g *GamePacket) Destination() net.IP {
	return net.IPv4(g.dst[0], g.dst[1], g.dst[2], g.dst[3])
}

func (g *GamePacket) SourceVirtualIp() net.IP {
	return net.IPv4(g.src[0], g.src[1], g.src[2], g.src[3])
}

func (g *GamePacket) EncodeHeader(header []byte) []byte {
	return g.encode(header, true)
}

func (g *GamePacket) EncodePacket(pool *sync.Pool) []byte {
	dataLength := int(g.Length + HeaderLength)
	data := pool.Get().([]byte)
	if cap(data) < dataLength {
		pool.Put(data)
		data = make([]byte, dataLength)
	}
	data = data[:0]
	data = g.encode(data, false)
	return data
}

func (g *GamePacket) EncodePacketWithBuffer(data []byte) []byte {
	data = g.encode(data, false)
	return data
}

func (g *GamePacket) ParseHeader(data []byte) error {
	return g.parse(data)
}

func (g *GamePacket) ParsePacket(pool *sync.Pool, content []byte) error {
	data := pool.Get().([]byte)
	if cap(data) < len(content) {
		pool.Put(data[:0])
		data = make([]byte, len(content))
	}
	data = data[:len(content)]
	copy(data, content)
	g.RawData = data
	return g.parse(data)
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
	g.src = [4]byte(data[9:13])
	g.Payload = data[13:]
	return nil
}
