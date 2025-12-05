package model

import "github.com/gdamore/tcell/v2"

type LogEntry struct {
	Index           string
	Timestamp       string
	Level           string
	LevelColor      tcell.Color
	Message         string
	OriginalMessage string
	CallID          string
}
