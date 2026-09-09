package ui

import "github.com/lxn/walk"

// participantRow is one row in the room-participants table.
type participantRow struct {
	Slot      int
	Name      string
	Ping      int
	Race      string
	BattleTag string
	Black     string // reason if blacklisted, else ""
}

type participantModel struct {
	walk.TableModelBase
	rows []participantRow
}

func (m *participantModel) RowCount() int { return len(m.rows) }

func (m *participantModel) Value(row, col int) interface{} {
	r := m.rows[row]
	switch col {
	case 0:
		return r.Slot
	case 1:
		return r.Name
	case 2:
		if r.Ping < 0 {
			return "-" // ping unavailable (in-game engine array has no latency)
		}
		return r.Ping
	case 3:
		return r.Race
	case 4:
		return r.BattleTag
	case 5:
		return r.Black
	}
	return nil
}

func (m *participantModel) SetRows(rows []participantRow) {
	m.rows = rows
	m.PublishRowsReset()
}

func (m *participantModel) RowAt(i int) (participantRow, bool) {
	if i < 0 || i >= len(m.rows) {
		return participantRow{}, false
	}
	return m.rows[i], true
}

// StyleCell colors the participant table: blacklisted players in red, ping by
// quality, and "(태그없음)" battleTags dimmed.
func (m *participantModel) StyleCell(style *walk.CellStyle) {
	row, ok := m.RowAt(style.Row())
	if !ok {
		return
	}
	if row.Black != "" {
		// Whole row highlighted for blacklisted users.
		style.BackgroundColor = walk.RGB(255, 214, 214)
		style.TextColor = walk.RGB(170, 0, 0)
		return
	}
	switch style.Col() {
	case 2: // ping
		switch {
		case row.Ping < 0:
			style.TextColor = walk.RGB(150, 150, 150) // unavailable (in-game)
		case row.Ping <= 40:
			style.TextColor = walk.RGB(0, 140, 0) // good: green
		case row.Ping <= 120:
			style.TextColor = walk.RGB(200, 130, 0) // ok: amber
		default:
			style.TextColor = walk.RGB(200, 0, 0) // bad: red
		}
	case 4: // battleTag
		if row.BattleTag == "(태그없음)" || row.BattleTag == "" {
			style.TextColor = walk.RGB(150, 150, 150) // dimmed
		}
	}
}

// blacklistRow is one row in the blacklist table.
type blacklistRow struct {
	ID     string // battleTag or name (the key)
	Name   string
	Reason string
	Added  string
}

type blacklistModel struct {
	walk.TableModelBase
	rows []blacklistRow
}

func (m *blacklistModel) RowCount() int { return len(m.rows) }

func (m *blacklistModel) Value(row, col int) interface{} {
	r := m.rows[row]
	switch col {
	case 0:
		return r.ID
	case 1:
		return r.Name
	case 2:
		return r.Reason
	case 3:
		return r.Added
	}
	return nil
}

func (m *blacklistModel) SetRows(rows []blacklistRow) {
	m.rows = rows
	m.PublishRowsReset()
}

func (m *blacklistModel) RowAt(i int) (blacklistRow, bool) {
	if i < 0 || i >= len(m.rows) {
		return blacklistRow{}, false
	}
	return m.rows[i], true
}

// StyleCell tints the reason column red so blacklist reasons stand out.
func (m *blacklistModel) StyleCell(style *walk.CellStyle) {
	if style.Col() == 2 { // reason
		style.TextColor = walk.RGB(170, 0, 0)
	}
}
