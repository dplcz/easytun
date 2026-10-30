package protocol

import (
	"easytun/internal/errorcode"
	"encoding/binary"
	"fmt"
	"net"
	"sync"

	"golang.org/x/crypto/chacha20poly1305"
)

const (
	TypeHandshake = iota + 1
	TypeNoiseHandshake
	TypeNoiseResponse
	TypeDnsRequest
	TypeDnsResponse
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
const AuthenticationTagLength = 16
const MaxLengthField = 1<<16 - 1
const MaxPacketSize = HeaderLength + MaxLengthField
const MaxPayloadSize = MaxLengthField - AuthenticationTagLength

type payloadLengthMode uint8

const (
	payloadLengthPlaintext payloadLengthMode = iota
	payloadLengthCiphertext
	payloadLengthDataCompat
)

type GamePacket struct {
	src     [4]byte
	dst     [4]byte
	PType   uint8
	Payload []byte
	RawData []byte
	Length  uint16
}

func NewGamePacket(src, dst [4]byte, pType uint8, payload []byte) *GamePacket {
	g := &GamePacket{}
	g.Reset(src, dst, pType, payload)
	return g
}

func (g *GamePacket) Reset(src, dst [4]byte, pType uint8, payload []byte) {
	g.src = src
	g.dst = dst
	g.PType = pType &^ FlagCompress
	g.Payload = payload
	g.Length, _ = payloadLengthField(len(payload))
}

func (g *GamePacket) CalculateMs() int {
	//TODO implement me
	panic("implement me")
}

// [magic 2][pType 1][dst 4][length 2][nonce 12(src 4 + label 8)][payload]

func (g *GamePacket) encode(b []byte, control bool, cipher *CipherState) []byte {
	length, ok := payloadLengthField(len(g.Payload))
	if !ok {
		return nil
	}
	g.Length = length

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
	if len(g.Payload) > 0 {
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
	mode := payloadLengthPlaintext
	if cipher != nil {
		mode = payloadLengthCiphertext
	} else if len(data) > 2 && data[2]&^FlagCompress == TypeData {
		// TypeData carries both initial plaintext Noise handshakes and encrypted
		// data in the current protocol, so retain both legacy length forms here.
		mode = payloadLengthDataCompat
	}
	pType, dst, src, length, err := parseHeader(data, mode)
	if err != nil {
		return err
	}
	g.PType = pType
	g.dst = dst
	g.src = src
	g.Length = length
	nonceBuf := data[9 : 9+NonceLength]
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

// payloadLengthField preserves the wire format used by existing clients: the
// length field includes the AEAD tag even for unencrypted control and DNS
// packets. Empty payloads are represented by a zero length.
func payloadLengthField(payloadLen int) (uint16, bool) {
	if payloadLen < 0 || payloadLen > MaxPayloadSize {
		return 0, false
	}
	if payloadLen == 0 {
		return 0, true
	}
	return uint16(payloadLen + AuthenticationTagLength), true
}

func parseHeader(data []byte, mode payloadLengthMode) (uint8, [4]byte, [4]byte, uint16, error) {
	var dst, src [4]byte
	if len(data) < HeaderLength {
		return 0, dst, src, 0, errorcode.PacketTooShort
	}
	if len(data) > MaxPacketSize {
		return 0, dst, src, 0, fmt.Errorf("%w: packet exceeds maximum size", errorcode.PayloadMismatch)
	}
	if binary.BigEndian.Uint16(data[:2]) != MagicNumber {
		return 0, dst, src, 0, errorcode.InvalidMagic
	}
	pType := data[2]
	if pType&FlagCompress != 0 && pType&^FlagCompress != TypeData {
		return 0, dst, src, 0, errorcode.PayloadMismatch
	}
	length := binary.BigEndian.Uint16(data[7:9])
	actualPayloadLen := len(data) - HeaderLength
	if !validPayloadLength(actualPayloadLen, length, mode) {
		return 0, dst, src, 0, errorcode.PayloadMismatch
	}
	dst = [4]byte(data[3:7])
	src = [4]byte(data[9 : 9+NonceLength])
	return pType, dst, src, length, nil
}

func validPayloadLength(actualPayloadLen int, length uint16, mode payloadLengthMode) bool {
	if length == 0 {
		return actualPayloadLen == 0
	}
	switch mode {
	case payloadLengthPlaintext:
		if length <= AuthenticationTagLength {
			return false
		}
		return actualPayloadLen == int(length)-AuthenticationTagLength
	case payloadLengthCiphertext:
		return actualPayloadLen == int(length)
	case payloadLengthDataCompat:
		return actualPayloadLen == int(length) ||
			(length > AuthenticationTagLength && actualPayloadLen == int(length)-AuthenticationTagLength)
	default:
		return false
	}
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
	if _, ok := payloadLengthField(len(g.Payload)); !ok {
		return nil
	}
	if compress {
		if g.compress(pool) {
			g.PType |= FlagCompress
		} else {
			g.PType &^= FlagCompress
		}
	}
	length, ok := payloadLengthField(len(g.Payload))
	if !ok {
		return nil
	}
	g.Length = length
	dataLength := HeaderLength + int(length)
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
	if len(content) > MaxPacketSize {
		return fmt.Errorf("%w: packet exceeds maximum size", errorcode.PayloadMismatch)
	}
	data := pool.Get().([]byte)
	if cap(data) < len(content) {
		pool.Put(data[:0])
		data = make([]byte, len(content))
	}
	data = data[:len(content)]
	copy(data, content)
	g.RawData = data
	if err := g.parse(data, parsePayload, cipher); err != nil {
		pool.Put(data[:0])
		g.RawData = nil
		g.Payload = nil
		return err
	}
	return nil
}

func (g *GamePacket) ParseControl(data []byte) error {
	pType, dst, src, length, err := parseHeader(data, payloadLengthPlaintext)
	if err != nil {
		g.Payload = nil
		return err
	}
	g.PType = pType
	g.dst = dst
	g.src = src
	g.Length = length
	g.Payload = data[9+NonceLength:]
	return nil
}
