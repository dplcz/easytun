package test

//func TestConcurrent(t *testing.T) {
//	// --- 配置参数 ---
//	targetAddr := "10.0.6.181:8080" // 目标 IP 和 端口
//	concurrency := 1                // 并发协程数
//	payloadSize := 1024             // 每个数据包的大小 (Byte)，这里设置 1KB
//	// ----------------
//
//	payload := make([]byte, payloadSize)
//	for i := range payload {
//		payload[i] = 'A' // 填充测试数据
//	}
//
//	var wg sync.WaitGroup
//	//startTime := time.Now()
//
//	fmt.Printf("开始对 %s 发起 UDP 测试...\n", targetAddr)
//	fmt.Printf("并发数: %d, 包大小: %d bytes\n", concurrency, payloadSize)
//
//	for i := 0; i < concurrency; i++ {
//		wg.Add(1)
//		go func(id int) {
//			defer wg.Done()
//
//			// 解析地址
//			addr, err := net.ResolveUDPAddr("udp", targetAddr)
//			if err != nil {
//				fmt.Printf("Worker %d 错误: %v\n", id, err)
//				return
//			}
//
//			// 建立 UDP "连接" (实际上只是绑定目标地址)
//			conn, err := net.DialUDP("udp", nil, addr)
//			if err != nil {
//				fmt.Printf("Worker %d 无法建立连接: %v\n", id, err)
//				return
//			}
//			defer conn.Close()
//
//			count := 0
//			for {
//				_, err := conn.Write(payload)
//				if err != nil {
//					// 常见的错误可能是本地缓冲区溢出
//					continue
//				}
//				count++
//
//				// 每发送 10000 个包打印一次进度（可选，频繁打印会降低性能）
//				if count%10000 == 0 {
//					fmt.Printf("Worker %d 已发送 %d 个包\n", id, count)
//				}
//				time.Sleep(100 * time.Millisecond)
//			}
//		}(i)
//	}
//
//	// 阻塞运行
//	wg.Wait()
//}
