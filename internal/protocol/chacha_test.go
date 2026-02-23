package protocol

import (
	"easytun/internal/config"
	"testing"
)

func TestChaCha(t *testing.T) {
	config.InitConfig("")
	InitChaCha()
	gp := NewGamePacket([4]byte{10, 0, 6, 222}, [4]byte{}, TypeData, []byte("TypeData"))
	encrypted := make([]byte, 2048)
	encrypted = gp.encode(encrypted[:0])

	err := gp.parse(encrypted, true)
	if err != nil {
		t.Fatal(err)
	}
}
