package ui

import (
	"fmt"
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"

	"github.com/hlauncher/hlauncher/internal/app"
	"github.com/hlauncher/hlauncher/internal/roster"
	"github.com/hlauncher/hlauncher/internal/version"
)

// gameWindow is the visible main window: room participants + blacklist manager.
type gameWindow struct {
	app          *app.App
	mw           *walk.MainWindow
	rosterModel  *participantModel
	blackModel   *blacklistModel
	rosterTV     *walk.TableView
	blackTV      *walk.TableView
	summaryLabel *walk.Label
}

func buildWindow(a *app.App) (*gameWindow, error) {
	w := &gameWindow{
		app:         a,
		rosterModel: &participantModel{},
		blackModel:  &blacklistModel{},
	}

	if err := (MainWindow{
		AssignTo: &w.mw,
		Title:    "H런처 " + version.Version,
		MinSize:  Size{Width: 780, Height: 520},
		Size:     Size{Width: 780, Height: 520},
		Layout:   VBox{},
		Children: []Widget{
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					Label{
						AssignTo: &w.summaryLabel,
						Text:     "대기 중…",
						Font:     Font{PointSize: 10, Bold: true},
					},
					HSpacer{},
				},
			},
			TabWidget{
				Pages: []TabPage{
					{
						Title:  "방 참가자",
						Layout: VBox{},
						Children: []Widget{
							TableView{
								AssignTo:         &w.rosterTV,
								AlternatingRowBG: true,
								ColumnsOrderable: true,
								Columns: []TableViewColumn{
									{Title: "슬롯", Width: 45},
									{Title: "닉네임", Width: 170},
									{Title: "핑", Width: 50},
									{Title: "종족", Width: 70},
									{Title: "배틀태그", Width: 180},
									{Title: "블랙 사유", Width: 190},
								},
								Model: w.rosterModel,
							},
							Composite{
								Layout: HBox{},
								Children: []Widget{
									PushButton{Text: "선택 참가자 블랙 등록", OnClicked: w.onBlackSelected},
									PushButton{Text: "새로고침", OnClicked: func() { w.refreshRoster() }},
									HSpacer{},
									Label{Text: "방에 들어가면 자동 갱신됩니다"},
								},
							},
						},
					},
					{
						Title:  "블랙리스트",
						Layout: VBox{},
						Children: []Widget{
							TableView{
								AssignTo:         &w.blackTV,
								AlternatingRowBG: true,
								ColumnsOrderable: true,
								Columns: []TableViewColumn{
									{Title: "식별자(배틀태그/이름)", Width: 200},
									{Title: "이름", Width: 150},
									{Title: "사유", Width: 230},
									{Title: "등록 시간", Width: 130},
								},
								Model: w.blackModel,
							},
							Composite{
								Layout: HBox{},
								Children: []Widget{
									PushButton{Text: "선택 삭제", OnClicked: w.onRemoveBlack},
									PushButton{Text: "새로고침", OnClicked: func() { w.refreshBlack() }},
									HSpacer{},
								},
							},
						},
					},
				},
			},
		},
	}).Create(); err != nil {
		return nil, err
	}

	// Apply cell coloring (blacklisted red, ping by quality, etc.).
	w.rosterTV.SetCellStyler(w.rosterModel)
	w.blackTV.SetCellStyler(w.blackModel)

	// Closing the window hides it to the tray instead of exiting.
	w.mw.Closing().Attach(func(canceled *bool, reason walk.CloseReason) {
		*canceled = true
		w.mw.SetVisible(false)
	})

	w.refreshBlack()
	return w, nil
}

// updateRoster is called on each room scan (from the app callback). frozen is
// true when parts is a last-lobby snapshot shown during an in-game match.
func (w *gameWindow) updateRoster(parts []roster.Participant, hits []app.BlackHit, frozen bool) {
	// index black reasons by participant identity
	reason := make(map[string]string)
	for _, h := range hits {
		reason[h.Participant.Name] = h.Entry.Reason
	}
	rows := make([]participantRow, 0, len(parts))
	for _, p := range parts {
		tag := p.BattleTag
		if tag == "" {
			tag = "(태그없음)"
		}
		rows = append(rows, participantRow{
			Slot: p.SlotID, Name: p.Name, Ping: p.Latency, Race: p.Race,
			BattleTag: tag, Black: reason[p.Name],
		})
	}
	w.rosterModel.SetRows(rows)
	w.updateSummary(len(parts), len(hits), frozen)
}

// updateSummary refreshes the top status bar.
func (w *gameWindow) updateSummary(roomCount, blackHits int, frozen bool) {
	tags := 0
	if w.app.Identity != nil {
		tags = w.app.Identity.Count()
	}
	blackText := ""
	if blackHits > 0 {
		blackText = fmt.Sprintf("   ·   ⚠ 방에 블랙 %d명", blackHits)
	}
	roomLabel := "방 인원"
	if frozen {
		roomLabel = "🎮 게임 중"
	}
	w.summaryLabel.SetText(fmt.Sprintf(
		"%s %d명%s   ·   축적된 배틀태그 %d개   ·   블랙리스트 %d명",
		roomLabel, roomCount, blackText, tags, w.app.Black.Count()))
}

func (w *gameWindow) refreshRoster() {
	// Show exactly what the scan loop last published (live lobby or frozen
	// in-game snapshot), so a manual refresh matches the automatic view.
	parts, hits, frozen := w.app.Displayed()
	w.updateRoster(parts, hits, frozen)
}

func (w *gameWindow) refreshBlack() {
	entries := w.app.Black.All()
	rows := make([]blacklistRow, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, blacklistRow{
			ID: e.BattleTag, Name: e.Name, Reason: e.Reason,
			Added: e.AddedAt.Format("2006-01-02 15:04"),
		})
	}
	w.blackModel.SetRows(rows)
}

func (w *gameWindow) onBlackSelected() {
	i := w.rosterTV.CurrentIndex()
	row, ok := w.rosterModel.RowAt(i)
	if !ok {
		walk.MsgBox(w.mw, "안내", "먼저 참가자를 선택하세요.", walk.MsgBoxIconInformation)
		return
	}
	reason, accepted := w.promptReason(row.Name)
	if !accepted {
		return
	}
	// Prefer battleTag as the key; fall back to name.
	tag := row.BattleTag
	if tag == "(태그없음)" || tag == "" {
		tag = ""
	}
	if err := w.app.AddBlack(tag, row.Name, reason); err != nil {
		walk.MsgBox(w.mw, "오류", err.Error(), walk.MsgBoxIconError)
		return
	}
	w.refreshBlack()
	w.refreshRoster()
}

func (w *gameWindow) onRemoveBlack() {
	i := w.blackTV.CurrentIndex()
	row, ok := w.blackModel.RowAt(i)
	if !ok {
		walk.MsgBox(w.mw, "안내", "먼저 블랙 항목을 선택하세요.", walk.MsgBoxIconInformation)
		return
	}
	if err := w.app.RemoveBlack(row.ID); err != nil {
		walk.MsgBox(w.mw, "오류", err.Error(), walk.MsgBoxIconError)
		return
	}
	w.refreshBlack()
	w.refreshRoster()
}

// promptReason shows a modal dialog to enter a blacklist reason.
func (w *gameWindow) promptReason(name string) (string, bool) {
	var dlg *walk.Dialog
	var edit *walk.LineEdit
	var okBtn, cancelBtn *walk.PushButton
	result := ""
	accepted := false

	_ = (Dialog{
		AssignTo:      &dlg,
		Title:         "블랙 등록",
		DefaultButton: &okBtn,
		CancelButton:  &cancelBtn,
		MinSize:       Size{Width: 360, Height: 140},
		Layout:        VBox{},
		Children: []Widget{
			Label{Text: fmt.Sprintf("[%s] 블랙 사유를 입력하세요:", name)},
			LineEdit{AssignTo: &edit},
			Composite{
				Layout: HBox{},
				Children: []Widget{
					HSpacer{},
					PushButton{
						AssignTo: &okBtn,
						Text:     "등록",
						OnClicked: func() {
							result = edit.Text()
							accepted = true
							dlg.Accept()
						},
					},
					PushButton{
						AssignTo:  &cancelBtn,
						Text:      "취소",
						OnClicked: func() { dlg.Cancel() },
					},
				},
			},
		},
	}).Create(w.mw)
	dlg.Run()
	return result, accepted
}

// show makes the window visible and brings it to the front.
func (w *gameWindow) show() {
	w.refreshRoster()
	w.refreshBlack()
	w.mw.SetVisible(true)
	w.mw.Show()
	win := w.mw.AsFormBase()
	_ = win
	w.mw.SetFocus()
}

var _ = time.Now
