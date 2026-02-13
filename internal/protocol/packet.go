package protocol

import (
	"bytes"
	"encoding/binary"
	"game_tun/internal/errorcode"
	"net"
	"sync"
)

const (
	TypeHandshake = iota + 1
	TypePing
	TypePong
	TypeData
)

const MagicNumber = 0xDAAA

const HeaderLength = 13

type GamePacket struct {
	src     [4]byte
	dst     [4]byte
	PType   uint8
	Payload []byte
	RawData []byte
	Length  uint16
}

func NewGamePacket(src, dst [4]byte, pType uint8, payload []byte) *GamePacket {
	return &GamePacket{
		src:     src,
		dst:     dst,
		PType:   pType,
		Length:  uint16(len(payload)),
		Payload: payload,
	}
}

func (g *GamePacket) CalculateMs() int {
	//TODO implement me
	panic("implement me")
}

// [magic 2][pType 1][src 4][dst 4][length 2][payload]

func (g *GamePacket) encode(b []byte) []byte {
	b = binary.BigEndian.AppendUint16(b, MagicNumber)
	b = append(b, g.PType)
	b = append(b, g.src[:]...)
	b = append(b, g.dst[:]...)
	b = binary.BigEndian.AppendUint16(b, g.Length)
	b = append(b, g.Payload...)
	return b
}

func (g *GamePacket) parse(data []byte, parsePayload bool) error {
	var magic uint16

	if len(data) < HeaderLength {
		return errorcode.PacketTooShort
	}
	reader := bytes.NewReader(data)

	if err := binary.Read(reader, binary.BigEndian, &magic); err != nil {
		return err
	}
	if magic != MagicNumber {
		return errorcode.InvalidMagic
	}
	if err := binary.Read(reader, binary.BigEndian, &g.PType); err != nil {
		return err
	}
	if err := binary.Read(reader, binary.BigEndian, &g.src); err != nil {
		return err
	}
	if err := binary.Read(reader, binary.BigEndian, &g.dst); err != nil {
		return err
	}
	if err := binary.Read(reader, binary.BigEndian, &g.Length); err != nil {
		return err
	}
	if parsePayload {
		g.Payload = data[HeaderLength : HeaderLength+int(g.Length)]
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

func (g *GamePacket) EncodeWithoutPool(data []byte) []byte {
	return g.encode(data)
}

func (g *GamePacket) ParsePacket(pool *sync.Pool, content []byte, parsePayload bool) error {
	if parsePayload {
		data := pool.Get().([]byte)
		if cap(data) < len(content) {
			pool.Put(data[:0])
			data = make([]byte, len(content))
		}
		data = data[:len(content)]
		copy(data, content)
		return g.parse(data, parsePayload)
	}
	return g.parse(content, parsePayload)

}
