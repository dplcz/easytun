package ui

import (
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/pterm/pterm"
)

func TestUi(t *testing.T) {
	sendHistory := make([]int, 5)
	recvHistory := make([]int, 5)

	area, _ := pterm.DefaultArea.Start()
	defer area.Stop()

	ticker := time.NewTicker(time.Second)
	for range ticker.C {
		// 模拟获取当前秒的原子计数器增量
		currSend := rand.Intn(500) + 100
		currRecv := rand.Intn(500) + 150

		// 2. 更新数值滑动窗口
		sendHistory = append(sendHistory[1:], currSend)
		recvHistory = append(recvHistory[1:], currRecv)

		// 3. 动态生成带有正确 Label 的 pterm.Bars
		// 我们通过循环，根据索引位置赋予不同的 Label
		var sendBars []pterm.Bar
		var recvBars []pterm.Bar

		for i := 0; i < 5; i++ {
			label := ""
			if i == 4 {
				label = "NOW" // 最后一根柱子标记为“现在”
			} else {
				label = fmt.Sprintf("-%ds", 4-i) // 前面的标记为 -4s, -3s...
			}

			sendBars = append(sendBars, pterm.Bar{Label: label, Value: sendHistory[i]})
			recvBars = append(recvBars, pterm.Bar{Label: label, Value: recvHistory[i], Style: pterm.NewStyle(pterm.FgLightGreen)})
		}

		// 4. 渲染图表
		sendChart, _ := pterm.DefaultBarChart.
			WithBars(sendBars).
			WithHorizontal(false).
			WithShowValue(true).
			Srender()

		recvChart, _ := pterm.DefaultBarChart.
			WithBars(recvBars).
			WithHorizontal(false).
			WithShowValue(true).
			Srender()

		// 5. 上下布局组装
		renderContent, _ := pterm.DefaultPanel.WithPanels(pterm.Panels{
			{{Data: pterm.LightCyan("📊 SEND PPS MONITOR")}},
			{{Data: sendChart}},
			{{Data: ""}},
			{{Data: pterm.LightGreen("📊 RECV PPS MONITOR")}},
			{{Data: recvChart}},
		}).WithPadding(1).Srender()

		area.Update(renderContent)
	}
}
