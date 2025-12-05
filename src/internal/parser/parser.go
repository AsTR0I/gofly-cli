package parser

import (
	"fmt"
	"gofly-cli/internal/model"
	"strings"

	"github.com/gdamore/tcell/v2"
)

func ParseLogLine(line string, index int) model.LogEntry {
	timestamp := ""
	if len(line) > 20 && line[0] == '[' {
		if idx := strings.Index(line, "]"); idx != -1 {
			timestamp = line[1:idx]
			line = strings.TrimSpace(line[idx+1:])
		}
	}

	levelText, levelColor := levelColored(line)

	messageText := decodeMessage(line)

	return model.LogEntry{
		Index:           fmt.Sprintf("%d", index),
		Timestamp:       timestamp,
		Level:           levelText,
		LevelColor:      levelColor,
		Message:         messageText,
		OriginalMessage: line,
	}
}

func levelColored(line string) (string, tcell.Color) {
	switch {
	case strings.Contains(line, "[DEBUG]"):
		return "DEBUG", tcell.ColorDarkCyan
	case strings.Contains(line, "[INFO]"):
		return "INFO", tcell.ColorGreen
	case strings.Contains(line, "[WARN]"):
		return "WARN", tcell.ColorYellow
	case strings.Contains(line, "[ERROR]"):
		return "ERROR", tcell.ColorRed
	case strings.Contains(line, "[WEB]"):
		return "WEB", tcell.ColorBlue
	default:
		return "", tcell.ColorWhite
	}
}

func decodeMessage(line string) string {
	line = strings.ReplaceAll(line, "[DEBUG]", "")
	line = strings.ReplaceAll(line, "[INFO]", "")
	line = strings.ReplaceAll(line, "[WARN]", "")
	line = strings.ReplaceAll(line, "[ERROR]", "")
	line = strings.ReplaceAll(line, "[WEB]", "")

	return strings.TrimSpace(line)
}
