package protocol

import (
	"bytes"
	"encoding/binary"
	"errors"
	"sync"
	"testing"

	"easytun/internal/errorcode"
	"golang.org/x/crypto/chacha20poly1305"
)

func packetTestPool() *sync.Pool {
	return &sync.Pool{New: func() any { return make([]byte, 256) }}
}

func TestPlainPacketLengthValidation(t *testing.T) {
	payload := []byte("plain payload")
	packet := NewGamePacket([4]byte{10, 0, 0, 1}, [4]byte{10, 0, 0, 2}, TypeDnsRequest, payload)
	wire := packet.EncodePacketWithBuffer(nil, false, nil)
	if got, want := binary.BigEndian.Uint16(wire[7:9]), uint16(len(payload)+AuthenticationTagLength); got != want {
		t.Fatalf("encoded length = %d, want %d", got, want)
	}

	parsed := &GamePacket{}
	pool := packetTestPool()
	if err := parsed.ParsePacket(pool, wire, true, nil); err != nil {
		t.Fatalf("ParsePacket rejected a valid plaintext packet: %v", err)
	}
	if !bytes.Equal(parsed.Payload, payload) {
		t.Fatalf("parsed payload = %q, want %q", parsed.Payload, payload)
	}
	pool.Put(parsed.RawData[:0])

	control := &GamePacket{}
	if err := control.ParseControl(wire); err != nil {
		t.Fatalf("ParseControl rejected a valid plaintext packet: %v", err)
	}
	if !bytes.Equal(control.Payload, payload) {
		t.Fatalf("control payload = %q, want %q", control.Payload, payload)
	}
}

func TestEncryptedPacketLengthValidation(t *testing.T) {
	aead, err := chacha20poly1305.New(make([]byte, chacha20poly1305.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	sender := &CipherState{aead: aead}
	receiver := &CipherState{aead: aead}
	payload := []byte("encrypted payload")
	packet := NewGamePacket([4]byte{10, 0, 0, 1}, [4]byte{10, 0, 0, 2}, TypeData, payload)
	wire := packet.EncodePacketWithBuffer(nil, false, sender)
	if got, want := len(wire)-HeaderLength, int(packet.Length); got != want {
		t.Fatalf("ciphertext length = %d, declared length = %d", got, want)
	}

	parsed := &GamePacket{}
	pool := packetTestPool()
	if err := parsed.ParsePacket(pool, wire, true, receiver); err != nil {
		t.Fatalf("ParsePacket rejected a valid encrypted packet: %v", err)
	}
	if !bytes.Equal(parsed.Payload, payload) {
		t.Fatalf("decrypted payload = %q, want %q", parsed.Payload, payload)
	}
	pool.Put(parsed.RawData[:0])
}

func TestDataPacketAcceptsLegacyPlaintextLengthForms(t *testing.T) {
	payload := []byte("Noise handshake")
	wire := NewGamePacket([4]byte{}, [4]byte{}, TypeData, payload).EncodePacketWithBuffer(nil, false, nil)

	for _, tc := range []struct {
		name        string
		lengthField uint16
	}{
		{name: "plaintext length includes tag", lengthField: uint16(len(payload) + AuthenticationTagLength)},
		{name: "plaintext length equals payload", lengthField: uint16(len(payload))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			packet := append([]byte(nil), wire...)
			binary.BigEndian.PutUint16(packet[7:9], tc.lengthField)
			parsed := &GamePacket{}
			if err := parsed.ParsePacket(packetTestPool(), packet, true, nil); err != nil {
				t.Fatalf("ParsePacket rejected a compatible TypeData packet: %v", err)
			}
			if !bytes.Equal(parsed.Payload, payload) {
				t.Fatalf("parsed payload = %q, want %q", parsed.Payload, payload)
			}
		})
	}
}

func TestParseRejectsMalformedPacketLengths(t *testing.T) {
	valid := NewGamePacket([4]byte{}, [4]byte{}, TypeDnsRequest, []byte("payload")).EncodePacketWithBuffer(nil, true, nil)

	tests := []struct {
		name string
		data []byte
	}{
		{name: "truncated header", data: valid[:HeaderLength-1]},
		{name: "truncated payload", data: valid[:len(valid)-1]},
		{name: "extra trailing byte", data: append(append([]byte(nil), valid...), 0)},
		{name: "zero length with payload", data: func() []byte {
			data := append([]byte(nil), valid...)
			binary.BigEndian.PutUint16(data[7:9], 0)
			return data
		}()},
		{name: "tag-only declaration", data: func() []byte {
			data := append([]byte(nil), valid[:HeaderLength]...)
			binary.BigEndian.PutUint16(data[7:9], AuthenticationTagLength)
			return data
		}()},
		{name: "control payload with extra tag-sized tail", data: func() []byte {
			data := append([]byte(nil), valid...)
			return append(data, make([]byte, AuthenticationTagLength)...)
		}()},
		{name: "compression flag on control packet", data: func() []byte {
			data := append([]byte(nil), valid...)
			data[2] = TypeDnsRequest | FlagCompress
			return data
		}()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := packetTestPool()
			parsed := &GamePacket{}
			if err := parsed.ParsePacket(pool, tt.data, true, nil); err == nil {
				t.Fatal("ParsePacket accepted malformed packet")
			} else if !errors.Is(err, errorcode.PacketTooShort) && !errors.Is(err, errorcode.InvalidMagic) && !errors.Is(err, errorcode.PayloadMismatch) {
				t.Fatalf("unexpected error: %v", err)
			}
			if parsed.RawData != nil {
				t.Fatal("ParsePacket retained a pooled buffer after failure")
			}
			if err := parsed.ParseControl(tt.data); err == nil {
				t.Fatal("ParseControl accepted malformed packet")
			}
		})
	}
}

func TestEncodeRejectsPayloadLengthOverflow(t *testing.T) {
	payload := make([]byte, MaxPayloadSize+1)
	packet := NewGamePacket([4]byte{}, [4]byte{}, TypeData, payload)
	if got := packet.EncodePacketWithBuffer(nil, true, nil); got != nil {
		t.Fatalf("encoded oversized payload into %d bytes", len(got))
	}
	if got := packet.EncodePacket(packetTestPool(), true, false, nil); got != nil {
		t.Fatalf("encoded oversized payload into %d bytes", len(got))
	}
}
