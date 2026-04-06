package ui

import (
	"easytun/internal/util"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/pterm/pterm"
)

const (
	INIT = iota
	TESTNAT
	CONNECT
	INTINETWORK
	RUNNING
	RECONNECTING
	CLOSING
)

var STAT map[uint32]string

type Info struct {
	key       string
	val       string
	colorFunc func(...any) string
}

type InfoSnapshot struct {
	infos []*Info
}
type Board struct {
	status      *uint32
	sendCounter *uint64
	recvCounter *uint64
	sendBytes   *uint64
	recvBytes   *uint64
	localIp     atomic.Value
	info        atomic.Value
}

func init() {
	STAT = make(map[uint32]string)
	STAT[INIT] = "Init"
	STAT[TESTNAT] = "Test Nat"
	STAT[CONNECT] = "Connecting Server"
	STAT[INTINETWORK] = "Init Virtual Work"
	STAT[RUNNING] = "Running"
	STAT[RECONNECTING] = "Reconnecting"
	STAT[CLOSING] = "Closing"
}

func NewBoard(status *uint32, sendCounter, recvCounter, sendBytes, recvBytes *uint64) *Board {
	infoSnapshot := &InfoSnapshot{
		infos: make([]*Info, 0),
	}
	newBoard := &Board{
		status:      status,
		sendCounter: sendCounter,
		recvCounter: recvCounter,
		sendBytes:   sendBytes,
		recvBytes:   recvBytes,
	}
	newBoard.info.Store(infoSnapshot)
	newBoard.localIp.Store([4]byte{0, 0, 0, 0})
	return newBoard
}

func (b *Board) InitLocalIp(localIp [4]byte) {
	b.localIp.Store(localIp)
}

func (b *Board) generateBar(sendHistory, recvHistory []int) (string, string) {
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

func (b *Board) generateInfo() string {
	baseStr := pterm.LightCyan("Virtual Ip: ") + fmt.Sprintf("%v\n", b.localIp.Load().([4]byte))
	infoSnapShot := b.info.Load().(*InfoSnapshot)
	for _, info := range infoSnapShot.infos {
		if info.colorFunc != nil {
			baseStr += pterm.LightCyan(fmt.Sprintf("%s: ", info.key)) + info.colorFunc(fmt.Sprintf("%s\n", info.val))
		} else {
			baseStr += pterm.LightCyan(fmt.Sprintf("%s: ", info.key)) + fmt.Sprintf("%s\n", info.val)
		}
	}
	baseStr += pterm.LightCyan("Send Bytes: " + fmt.Sprintf("%s\n", util.FormatBytes(atomic.LoadUint64(b.sendBytes))))
	baseStr += pterm.LightCyan("Recv Bytes: " + fmt.Sprintf("%s\n", util.FormatBytes(atomic.LoadUint64(b.recvBytes))))

	return pterm.DefaultBox.WithTitle("System Info").Sprint(baseStr)
}

func (b *Board) PerformanceUi() {
	runningFlag := true
	ticker := time.NewTicker(time.Second)
	sendHistory := make([]int, 5)
	recvHistory := make([]int, 5)
	var lastSend uint64
	var lastRecv uint64
	area, _ := pterm.DefaultArea.Start()
	defer area.Stop()
	defer ticker.Stop()
	for runningFlag {
		select {
		case <-ticker.C:
			versionInfo := b.generateInfo()
			curSend := atomic.LoadUint64(b.sendCounter)
			curRecv := atomic.LoadUint64(b.recvCounter)
			curStatus := atomic.LoadUint32(b.status)
			sendHistory = append(sendHistory[1:], int(curSend-lastSend))
			recvHistory = append(recvHistory[1:], int(curRecv-lastRecv))
			lastSend = curSend
			lastRecv = curRecv
			sendBar, recvBar := b.generateBar(sendHistory, recvHistory)
			leftPanel, _ := pterm.DefaultPanel.WithPanels(pterm.Panels{
				{{Data: pterm.LightCyan("📊 SEND PPS MONITOR")}},
				{{Data: sendBar}},
				{{Data: ""}},
				{{Data: pterm.LightGreen("📊 RECV PPS MONITOR")}},
				{{Data: recvBar}},
			}).Srender()

			statusBox := pterm.DefaultBox.WithTitle("Status").Sprint(pterm.LightCyan("Status: ") + pterm.LightYellow(STAT[curStatus]))
			rightTable, _ := pterm.DefaultTable.WithHasHeader(false).WithBoxed(true).WithData(pterm.TableData{{versionInfo}, {statusBox}}).Srender()
			//rightPanel, _ := pterm.DefaultPanel.WithPanels(pterm.Panels{
			//	{{Data: versionInfo}},
			//	{{Data: ""}},
			//	{{Data: statusBox}},
			//}).Srender()
			renderContent, _ := pterm.DefaultPanel.WithPanels(pterm.Panels{
				{{Data: leftPanel}, {Data: rightTable}},
			}).WithPadding(2).Srender()
			area.Update(renderContent)
			if curStatus == CLOSING {
				runningFlag = false
			}
		}
	}
}

func (b *Board) AddInfo(key, val string, colorFunc func(...any) string) {
	oldSnapshot := b.info.Load().(*InfoSnapshot)
	newSnapshot := &InfoSnapshot{
		infos: make([]*Info, 0, len(oldSnapshot.infos)+1),
	}
	for _, info := range oldSnapshot.infos {
		newSnapshot.infos = append(newSnapshot.infos, info)
	}
	newSnapshot.infos = append(newSnapshot.infos, &Info{
		key:       key,
		val:       val,
		colorFunc: colorFunc,
	})
	b.info.Store(newSnapshot)
}
