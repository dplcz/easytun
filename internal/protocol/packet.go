package protocol

import (
	"bytes"
	"encoding/binary"
	"game_tun/internal/errorcode"
	"log"
)

const (
	TypeHandshake = iota + 1
	TypeKeepAlive
	TypeData
)

const MagicNumber = 0xDAAA

type GamePacket struct {
	src     uint8
	dst     uint8
	pType   uint8
	Payload []byte
}

type Packet interface {
	CalculateMs() int
	Encode() ([]byte, error)
	Decode([]byte) error
}

func NewGamePacket(src, dst, pType uint8, payload []byte) *GamePacket {
	return &GamePacket{
		src:     src,
		dst:     dst,
		pType:   pType,
		payLoad: payload,
	}
}

func (g GamePacket) CalculateMs() int {
	//TODO implement me
	panic("implement me")
}

// [magic 2][pType 1][src 1][dst 1][length 2][payload]

func (g GamePacket) Encode() ([]byte, error) {
	buf := bytes.NewBuffer(make([]byte, 0))
	if err := binary.Write(buf, binary.BigEndian, uint16(MagicNumber)); err != nil {
		log.Println(err)
		return nil, nil
	}
	if err := binary.Write(buf, binary.BigEndian, g.pType); err != nil {
		log.Println(err)
		return nil, nil
	}
	if err := binary.Write(buf, binary.BigEndian, g.src); err != nil {
		log.Println(err)
		return nil, nil
	}
	if err := binary.Write(buf, binary.BigEndian, g.dst); err != nil {
		log.Println(err)
		return nil, nil
	}
	length := len(g.payLoad)
	if err := binary.Write(buf, binary.BigEndian, uint16(length)); err != nil {
		log.Println(err)
		return nil, nil
	}
	if length > 0 {
		if err := binary.Write(buf, binary.BigEndian, g.payLoad); err != nil {
			log.Println(err)
			return nil, nil
		}
	}
	return buf.Bytes(), nil
}

func (g GamePacket) Decode(data []byte) error {
	var magic uint16
	var src uint8
	var dst uint8
	var pType uint8
	var length uint16

	if len(data) < 7 {
		return errorcode.PacketTooShort
	}
	reader := bytes.NewReader(data)

	if err := binary.Read(reader, binary.BigEndian, &magic); err != nil {
		return err
	}
	if magic != MagicNumber {
		return errorcode.InvalidMagic
	}
	if err := binary.Read(reader, binary.BigEndian, &pType); err != nil {
		return err
	}
	if err := binary.Read(reader, binary.BigEndian, &src); err != nil {
		return err
	}
	if err := binary.Read(reader, binary.BigEndian, &dst); err != nil {
		return err
	}
	if err := binary.Read(reader, binary.BigEndian, &length); err != nil {
		return err
	}
	payload := make([]byte, length)
	if length > 0 {
		// 检查剩余数据是否足够
		if int(length) > reader.Len() {
			return errorcode.PayloadMismatch
		}
		reader.Read(payload)
	}
	g.src = src
	g.dst = dst
	g.pType = pType
	g.payLoad = payload
	return nil
}
