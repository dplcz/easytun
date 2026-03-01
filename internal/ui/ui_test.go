package ui

import (
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/pterm/pterm"
)

func TestUi(t *testing.T) {
	// 1. 设置数据容器
	throughputData := []pterm.Bar{
		{Label: "T-9", Value: 10},
		{Label: "T-8", Value: 12},
		{Label: "T-7", Value: 15},
		{Label: "T-6", Value: 11},
		{Label: "T-5", Value: 18},
	}

	// 2. 创建一个实时更新区域
	area, _ := pterm.DefaultArea.Start()
	defer area.Stop()

	for i := 0; i < 50; i++ {
		// 模拟数据生成
		newVal := rand.Intn(20) + 10
		latency := rand.Intn(100) + 20

		// 更新吞吐量队列（保持最近 10 个数据点）
		throughputData = append(throughputData, pterm.Bar{
			Label: fmt.Sprintf("%ds", i),
			Value: newVal,
		})
		if len(throughputData) > 10 {
			throughputData = throughputData[1:]
		}

		// 3. 构建 UI 布局
		// 顶部：吞吐量折线图
		chart, _ := pterm.DefaultBarChart.
			WithBars(throughputData).
			WithHorizontal(false). // 纵向排列模拟折线趋势
			WithHeight(10).
			Srender()

		// 底部：延迟仪表盘样式
		var latencyColor = pterm.FgGreen
		if latency > 80 {
			latencyColor = pterm.FgRed
		}

		dashboard := pterm.DefaultBox.WithTitle("实时仪表盘").Sprint(
			pterm.LightBlue("吞吐量: "), pterm.Bold.Sprintf("%d req/s\n", newVal),
			pterm.LightCyan("网络延迟: "), latencyColor.Sprintf("%d ms", latency),
		)

		// 4. 将所有内容合并发送到 Area
		area.Update(chart + "\n" + dashboard)

		time.Sleep(time.Millisecond * 500)
	}
}
