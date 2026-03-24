package ui

import (
	"context"
	"easytun/internal/config"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/pterm/pterm"
)

func generateBar(sendHistory, recvHistory []int) (string, string) {
	var sendBars []pterm.Bar
	var recvBars []pterm.Bar

	for i := 0; i < 5; i++ {
		label := ""
		if i == 4 {
			label = "NOW"
		} else {
			label = fmt.Sprintf("  -%ds  ", 4-i)
		}

		sendBars = append(sendBars, pterm.Bar{Label: label, Value: sendHistory[i]})
		recvBars = append(recvBars, pterm.Bar{Label: label, Value: recvHistory[i], Style: pterm.NewStyle(pterm.FgLightGreen)})
	}

	sendChart, _ := pterm.DefaultBarChart.
		WithBars(sendBars).
		WithHorizontal(false).
		WithShowValue(true).
		WithHeight(7).
		Srender()

	recvChart, _ := pterm.DefaultBarChart.
		WithBars(recvBars).
		WithHorizontal(false).
		WithShowValue(true).
		WithHeight(7).
		Srender()
	return sendChart, recvChart
}

func PerformanceUi(ctx context.Context, sendCounter, recvCounter *uint64, status *uint8) {
	fmt.Print("\033[H\033[2J")
	ticker := time.NewTicker(time.Second)
	sendHistory := make([]int, 5)
	recvHistory := make([]int, 5)
	var lastSend uint64
	var lastRecv uint64
	area, _ := pterm.DefaultArea.Start()
	var p2pStatus string
	if config.EnableP2P {
		p2pStatus = pterm.LightGreen("Enable")
	} else {
		p2pStatus = pterm.LightRed("Disable")
	}
	versionInfo := pterm.DefaultBox.WithTitle("System Info").Sprint(
		pterm.LightCyan("Name: ") + fmt.Sprintf("%s\n", config.DeviceName) +
			pterm.LightCyan("Receiver Count: ") + pterm.LightCyan(fmt.Sprintf("%d\n", config.RecvWorkers)) +
			pterm.LightCyan("Sender Count: ") + pterm.LightCyan(fmt.Sprintf("%d\n", config.SendWorkers)) +
			pterm.LightCyan("P2P Status: ") + fmt.Sprintf("%s", p2pStatus),
	)
	defer area.Stop()
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			curSend := atomic.LoadUint64(sendCounter)
			curRecv := atomic.LoadUint64(recvCounter)
			sendHistory = append(sendHistory[1:], int(curSend-lastSend))
			recvHistory = append(recvHistory[1:], int(curRecv-lastRecv))
			lastSend = curSend
			lastRecv = curRecv
			sendBar, recvBar := generateBar(sendHistory, recvHistory)
			leftPanel, _ := pterm.DefaultPanel.WithPanels(pterm.Panels{
				{{Data: pterm.LightCyan("📊 SEND PPS MONITOR")}},
				{{Data: sendBar}},
				{{Data: ""}},
				{{Data: pterm.LightGreen("📊 RECV PPS MONITOR")}},
				{{Data: recvBar}},
			}).Srender()
			renderContent, _ := pterm.DefaultPanel.WithPanels(pterm.Panels{
				{{Data: leftPanel}, {Data: versionInfo}},
			}).WithPadding(2).Srender()
			area.Update(renderContent)
		case <-ctx.Done():
			return
		}
	}
}
