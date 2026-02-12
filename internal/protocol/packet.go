package protocol

import (
	"bytes"
	"encoding/binary"
	"game_tun/internal/errorcode"
	"log"
	"net"
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
	Length  uint16
}

type Packet interface {
	CalculateMs() int
	Encode() ([]byte, error)
	Decode([]byte) error
}

func NewGamePacket(src, dst [4]byte, pType uint8, payload []byte) *GamePacket {
	return &GamePacket{
		src:     src,
		dst:     dst,
		PType:   pType,
		Payload: payload,
	}
}

func (g *GamePacket) CalculateMs() int {
	//TODO implement me
	panic("implement me")
}

// [magic 2][pType 1][src 4][dst 4][length 2][payload]

func (g *GamePacket) Encode() []byte {
	buf := bytes.NewBuffer(make([]byte, 0))
	if err := binary.Write(buf, binary.BigEndian, uint16(MagicNumber)); err != nil {
		log.Println(err)
		return nil
	}
	if err := binary.Write(buf, binary.BigEndian, g.PType); err != nil {
		log.Println(err)
		return nil
	}
	if err := binary.Write(buf, binary.BigEndian, g.src); err != nil {
		log.Println(err)
		return nil
	}
	if err := binary.Write(buf, binary.BigEndian, g.dst); err != nil {
		log.Println(err)
		return nil
	}
	length := len(g.Payload)
	if err := binary.Write(buf, binary.BigEndian, uint16(length)); err != nil {
		log.Println(err)
		return nil
	}
	if length > 0 {
		if err := binary.Write(buf, binary.BigEndian, g.Payload); err != nil {
			log.Println(err)
			return nil
		}
	}
	return buf.Bytes()
}

func (g *GamePacket) Parse(data []byte, parsePayload bool) error {
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
		var payload []byte
		if g.Length > 0 {
			if int(g.Length) > reader.Len() {
				return errorcode.PayloadMismatch
			}
			payload = make([]byte, int(g.Length))
			reader.Read(payload)
			g.Payload = payload
		}
		// TODO 未来实现零拷贝
	}
	return nil
}

func (g *GamePacket) Destination() net.IP {
	return net.IPv4(g.dst[0], g.dst[1], g.dst[2], g.dst[3])
}

func (g *GamePacket) SourceVirtualIp() net.IP {
	return net.IPv4(g.src[0], g.src[1], g.src[2], g.src[3])
}
