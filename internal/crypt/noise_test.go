package crypt

import (
	"crypto/rand"
	"fmt"
	"log"
	"testing"

	"github.com/flynn/noise"
)

func TestNoise(t *testing.T) {
	cs := noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashSHA256)
	keyI, _ := cs.GenerateKeypair(rand.Reader)
	keyR, _ := cs.GenerateKeypair(rand.Reader)

	// --- 第一阶段：初始化状态机 ---

	// Initiator 配置 (需要预知 Responder 的公钥 keyR.Public)
	initHS, _ := noise.NewHandshakeState(noise.Config{
		CipherSuite:   cs,
		Pattern:       noise.HandshakeIK,
		Initiator:     true,
		StaticKeypair: keyI,
		PeerStatic:    keyR.Public,
	})

	// Responder 配置
	respHS, _ := noise.NewHandshakeState(noise.Config{
		CipherSuite:   cs,
		Pattern:       noise.HandshakeIK,
		Initiator:     false,
		StaticKeypair: keyR,
	})

	fmt.Println(">>> 开始握手")

	// --- 第二阶段：握手第一个包 (Initiator -> Responder) ---
	// 包含: e, es, s, ss
	msg1, _, _, err := initHS.WriteMessage(nil, nil)
	if err != nil {
		log.Fatal("Initiator WriteMessage 1 失败:", err)
	}
	fmt.Printf("1. Initiator 发送握手包 (%d bytes)\n", len(msg1))

	// Responder 读取第一个包
	_, _, _, err = respHS.ReadMessage(nil, msg1)
	if err != nil {
		log.Fatal("Responder ReadMessage 1 失败:", err)
	}
	fmt.Println("2. Responder 已读取并解析握手包")

	// --- 第三阶段：握手第二个包 (Responder -> Initiator) ---
	// 包含: e, ee, se
	// 注意：在 IK 模式下，这次调用后会返回加密状态对象 (CipherState)
	msg2, respSend, respRecv, err := respHS.WriteMessage(nil, nil)
	if err != nil {
		log.Fatal("Responder WriteMessage 2 失败:", err)
	}
	fmt.Printf("3. Responder 发送响应包 (%d bytes)\n", len(msg2))

	// Initiator 读取第二个包并获取加密状态对象
	_, initRecv, initSend, err := initHS.ReadMessage(nil, msg2)
	if err != nil {
		log.Fatal("Initiator ReadMessage 2 失败:", err)
	}
	fmt.Println("4. Initiator 已读取响应包，握手正式完成")

	fmt.Println("\n>>> 开始加密传输测试")

	// --- 第四阶段：加密数据传输 (Initiator -> Responder) ---

	plaintext := []byte("")
	// 使用 initSend 加密
	ciphertext, err := initSend.Encrypt(nil, []byte{1, 2, 34}, plaintext)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("5. Initiator 加密后数据长度: %d bytes\n", len(ciphertext))

	// 使用 respRecv 解密
	decrypted, err := respRecv.Decrypt(nil, []byte{1, 2, 34}, ciphertext)
	if err != nil {
		log.Fatal("6. Responder 解密失败（这就是你之前报错的地方）:", err)
	}
	fmt.Printf("6. Responder 成功解密: %s\n", string(decrypted))

	// --- 第五阶段：反向加密传输 (Responder -> Initiator) ---

	replyText := []byte("收到，消息已确认")
	ciphertextReply, _ := respSend.Encrypt(nil, nil, replyText)

	decryptedReply, err := initRecv.Decrypt(nil, nil, ciphertextReply)
	if err != nil {
		log.Fatal("7. Initiator 解密回显失败:", err)
	}
	fmt.Printf("7. Initiator 成功解密回显: %s\n", string(decryptedReply))
}
