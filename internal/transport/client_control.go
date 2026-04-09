//go:build client

package transport

import (
	"bytes"
	"context"
	"easytun/internal/config"
	"easytun/internal/protocol"
	"easytun/internal/util"
	"fmt"
	"log"
	"net"
	"time"

	"github.com/gorilla/websocket"
)

// connectServer 初始化与服务器的 WebSocket 控制连接及本地 UDP 数据连接
func (t *ClientTransport) connectServer() error {
	wsUrl := fmt.Sprintf("ws://%s:%d/ws", config.ServerIp, config.ServerPort)
	tempControlConn, _, err := websocket.DefaultDialer.Dial(wsUrl, nil)
	if err != nil {
		return err
	}
	t.controlConn = tempControlConn

	serverAddr, _ := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", config.ServerIp, config.ServerPort))
	t.serverAddr = serverAddr
	checkAddr, _ := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", config.ServerIp, config.CheckPort))
	t.checkAddr = checkAddr
	localAddr, _ := net.ResolveUDPAddr("udp", ":0")
	conn, err := net.ListenUDP("udp", localAddr)
	if err != nil {
		return err
	}
	conn.SetReadBuffer(4 * 1024 * 1024)
	conn.SetWriteBuffer(4 * 1024 * 1024)
	t.dataConn = conn
	return nil
}

// handshake 与服务端执行初始握手，交换 NAT 类型和 Noise 公钥，并获取分配的虚拟 IP
func (t *ClientTransport) handshake() (*protocol.GamePacket, error) {
	payload := append([]byte{t.natType}, t.noiseMgr.GetPublicKey()...)
	payload = append(payload, []byte(t.device.Name())...)
	handshakePacket := protocol.NewGamePacket([4]byte{}, [4]byte{}, protocol.TypeHandshake, payload)
	data := handshakePacket.EncodePacket(t.bufPool, true, false, nil)
	err := t.controlConn.WriteMessage(websocket.BinaryMessage, data)
	t.bufPool.Put(data[:0])
	if err != nil {
		return nil, err
	}
	t.controlConn.SetReadDeadline(time.Now().Add(time.Second * config.ReadTimeout))
	_, content, err := t.controlConn.ReadMessage()
	if err != nil {
		return nil, err
	}
	err = handshakePacket.ParsePacket(t.bufPool, content, true, nil)
	if err != nil {
		return nil, err
	}
	return handshakePacket, nil
}

// heartbeat 定时发送 WebSocket 和 UDP 层的心跳包，保持连接活跃
func (t *ClientTransport) heartbeat(ctx context.Context) error {
	timer := time.NewTicker(time.Second * config.PingTime)
	defer timer.Stop()
	wsHeartbeatPacket := protocol.NewGamePacket([4]byte(t.localIp.To4()), [4]byte{}, protocol.TypePing, nil)
	udpHeartbeatPacket := protocol.NewGamePacket([4]byte(t.localIp.To4()), [4]byte{}, protocol.TypePing, nil)
	udpHeartbeatBytes := udpHeartbeatPacket.EncodePacket(t.bufPool, true, false, nil)
	defer t.bufPool.Put(udpHeartbeatBytes[:0])
	for {
		select {
		case <-timer.C:
			t.ControlSendChan <- wsHeartbeatPacket
			if _, err := t.dataConn.WriteToUDP(udpHeartbeatBytes, t.serverAddr); err != nil {
				return fmt.Errorf("UDP 心跳发送失败: %w", err)
			}
		case <-ctx.Done():
			return nil
		}
	}
}

// controlRecv 持续监听并处理来自 WebSocket 的服务器控制消息
func (t *ClientTransport) controlRecv(ctx context.Context) error {
	defer t.controlConn.Close()
	gp := &protocol.GamePacket{}
	readBuffer := bytes.NewBuffer(make([]byte, 512))
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
			t.controlConn.SetReadDeadline(time.Now().Add(time.Second * config.ReadTimeout))
			t.controlConn.SetPongHandler(func(string) error {
				t.controlConn.SetReadDeadline(time.Now().Add(time.Second * config.ReadTimeout))
				return nil
			})
			msgType, reader, err := t.controlConn.NextReader()
			if err != nil {
				return fmt.Errorf("WebSocket 读取错误: %w", err)
			}
			if msgType == websocket.BinaryMessage {
				readBuffer.Reset()
				_, err = readBuffer.ReadFrom(reader)
				if err != nil {
					log.Println("Read buffer error:", err)
					continue
				}
				err = gp.ParseControl(readBuffer.Bytes())
				if err != nil {
					log.Println(err)
					continue
				}
				switch gp.PType {
				case protocol.TypeNoiseResponse:
					targetVip := gp.SourceVirtualIp()
					remotePub := gp.Payload[:32]
					pendingData := gp.Payload[32:]
					t.initiateNoise(util.IpToKey(targetVip), remotePub, pendingData)
				case protocol.TypeP2PCheck:
					log.Println("收到 check")
					t.checkP2P(gp.SourceVirtualIp())
				case protocol.TypeP2PCommand:
					t.addP2P([4]byte(gp.SourceVirtualIp().To4()), util.BytesToIP(gp.Payload[:6]), gp.Payload[6])
				default:
					continue
				}
			}
		}
	}
}

// controlSend 从通道读取控制消息并将其发送到 WebSocket 服务端
func (t *ClientTransport) controlSend(ctx context.Context) error {
	for {
		select {
		case gp := <-t.ControlSendChan:
			if gp.PType == protocol.TypePing {
				err := t.controlConn.WriteMessage(websocket.PingMessage, nil)
				if err != nil {
					return fmt.Errorf("WebSocket Ping 发送失败: %w", err)
				}
			} else {
				data := gp.EncodePacket(t.bufPool, true, false, nil)
				err := t.controlConn.WriteMessage(websocket.BinaryMessage, data)
				t.bufPool.Put(data[:0])
				if err != nil {
					return fmt.Errorf("WebSocket 消息发送失败: %w", err)
				}
			}
		case <-ctx.Done():
			return nil
		}
	}
}
