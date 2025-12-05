package main

import (
	"bufio"
	"flag"
	"fmt"
	"gofly-cli/internal/model"
	"gofly-cli/internal/parser"
	"net"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

var (
	appVersion  = "25.12.4"
	activeLogs  int
	tsRegexp    = regexp.MustCompile(`^\[(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2})\]`)
	hotBar      *tview.TextView
	input       *tview.InputField
	searchTimer *time.Timer
	isSearching bool

	logTable   *tview.Table
	statusBar  *tview.Flex
	app        *tview.Application
	flex       *tview.Flex
	serverAddr string
	// хранения всех логов и фильтра
	allLogs       []model.LogEntry
	currentFilter string
	currentMode   string
	inputFile     string
	// буфер для обработки файлов
	logBatch  []model.LogEntry
	batchSize = 100

	sessionColors = make(map[string]tcell.Color)
	colorIndex    = 0

	lastSubAck    = time.Now()
	isConnected   = false
	alreadyWarned = false

	autoScroll = false
)

func main() {
	ip := flag.String("ip", "127.0.0.1", "Set custom server IP. [-ip %host%]")
	port := flag.String("port", "9090", "Set custom server PORT. [-port %port%]")
	help := flag.Bool("h", false, "Prints flags and their descriptions")
	inputFilePtr := flag.String("I", "", "Change the mod of app from connect and reading UDP to parse the FILE. [-I %path to file%]")
	flag.Parse()

	if *help {
		fmt.Println("gofly-cli — CLI for gofly")
		fmt.Println("Usage: gofly-cli [-h | -I filename | [-ip host][-port port]]")
		flag.PrintDefaults()
		os.Exit(0)
	}

	serverAddr = fmt.Sprintf("%s:%s", *ip, *port)
	inputFile = *inputFilePtr
	allLogs = make([]model.LogEntry, 0)
	logBatch = make([]model.LogEntry, 0, batchSize)

	if inputFile != "" {
		currentMode = fmt.Sprintf("File [%s]", inputFile)
	} else {
		currentMode = "Online"
	}

	// Tview App
	app = tview.NewApplication()

	// Status bar
	statusBar = tview.NewFlex().SetDirection(tview.FlexRow)

	// First row - main info
	firstLine := tview.NewFlex().SetDirection(tview.FlexColumn)

	infoText := tview.NewTextView().
		SetDynamicColors(true)

	// Set title by mode
	if inputFile != "" {
		infoText.SetText(fmt.Sprintf(
			"gofly-cli v.%s    Current Mode: %s    Logs: %d    Match Expressions: 0",
			appVersion, currentMode, activeLogs))
	} else {
		infoText.SetText(fmt.Sprintf(
			"gofly-cli v.%s    Current Mode: %s    [%s]    Logs: %d    Match Expressions: 0",
			appVersion, currentMode, serverAddr, activeLogs))
	}

	firstLine.AddItem(infoText, 0, 1, false)

	// Second row - filter
	secondLine := tview.NewFlex().SetDirection(tview.FlexColumn)

	filterLabel := tview.NewTextView().
		SetDynamicColors(true).
		SetText("Display Filter:")

	input = tview.NewInputField().
		SetPlaceholder("Enter filter text...").
		SetFieldWidth(50).
		SetChangedFunc(func(text string) {

			if !isSearching && text != "" {
				isSearching = true
				updateStatusBar(len(allLogs), "typing...")
			}

			if searchTimer != nil {
				searchTimer.Stop()
			}

			searchTimer = time.AfterFunc(750*time.Millisecond, func() {
				app.QueueUpdateDraw(func() {
					isSearching = false
					currentFilter = strings.TrimSpace(text)
					if currentFilter != "" {
						applyFilter(currentFilter)
					} else {
						clearFilter()
					}
				})
			})
		}).
		SetDoneFunc(func(key tcell.Key) {
			if searchTimer != nil {
				searchTimer.Stop()
				isSearching = false
			}

			switch key {
			case tcell.KeyEnter:
				currentFilter = strings.TrimSpace(input.GetText())
				if currentFilter != "" {
					applyFilter(currentFilter)
				} else {
					clearFilter()
				}
				app.SetFocus(logTable)
			case tcell.KeyEsc:
				input.SetText("")
				clearFilter()
				app.SetFocus(logTable)
			}
		})

	secondLine.
		AddItem(filterLabel, 15, 1, false).
		AddItem(input, 50, 1, false)

	// build the status bar
	statusBar.
		AddItem(firstLine, 1, 1, false).
		AddItem(secondLine, 1, 1, false)

	statusBar.SetBorder(true).SetTitle(" Status ")

	//  Log table
	logTable = tview.NewTable().
		SetBorders(false).
		SetFixed(1, 0)

	headers := []string{"Idx", "Time", "Level", "Message"}
	for i, h := range headers {
		cell := tview.NewTableCell(h).
			SetTextColor(tcell.ColorYellow).
			SetAlign(tview.AlignCenter).
			SetSelectable(false)

		switch i {
		case 0: // Idx - 5 symb
			cell.SetExpansion(0)
		case 1: // Time - 20 symb
			cell.SetExpansion(0)
		case 2: // Level - 10 symb
			cell.SetExpansion(0)
		case 3: // Message - all space
			cell.SetExpansion(1)
		}

		logTable.SetCell(0, i, cell)
	}
	logTable.SetSelectable(true, false)

	//  bottom (hottab)
	hotBar = tview.NewTextView().SetDynamicColors(true)
	hotBar.
		SetDynamicColors(true).
		SetText("[yellow]Esc\\Q[-] Quit   [green]F1[-] Help   [green]F3[-] Focus Filter   [green]F4[-] Clear Filter   [green]F5[-] Clear   [red]F6[-] AutoScroll")
	hotBar.SetBorder(true)
	hotBar.SetTitle(" Hotkeys ")

	flex = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(statusBar, 4, 1, false).
		AddItem(logTable, 0, 1, true).
		AddItem(hotBar, 3, 1, false)

	//  hot tabs
	app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyEsc:
			if app.GetFocus() == input {
				input.SetText("")
				app.SetFocus(logTable)
			} else {
				app.Stop()
				os.Exit(0)
			}
		case tcell.KeyF1:
			showHelp()
		case tcell.KeyF3:
			app.SetFocus(input)
		case tcell.KeyF4:
			input.SetText("")
			clearFilter()
		case tcell.KeyF5:
			clearLogs()
		case tcell.KeyF6:
			autoScroll = !autoScroll
			updateHotBar(hotBar, autoScroll)
			app.Draw()
		}

		switch event.Rune() {
		case 'q', 'Q':
			if app.GetFocus() != input {
				app.Stop()
				os.Exit(0)
				return nil
			}
		}

		return event
	})

	//  from file данных
	if inputFile != "" {
		if _, err := os.Stat(inputFile); err == nil {
			go func() {
				file, err := os.Open(inputFile)
				if err != nil {
					panic(err)
				}
				defer file.Close()

				scanner := bufio.NewScanner(file)
				buf := make([]byte, 0, 64*1024)
				scanner.Buffer(buf, 1024*1024)

				for scanner.Scan() {
					line := scanner.Text()
					processLine(line)
				}
				if len(logBatch) > 0 {
					flushLogBatch()
				}
			}()
		} else {
			fmt.Printf("File %s not found\n", inputFile)
			os.Exit(1)
		}
	} else {
		//  UDP mode: real time
		laddr, _ := net.ResolveUDPAddr("udp", ":0")
		conn, err := net.ListenUDP("udp", laddr)
		if err != nil {
			fmt.Printf("[ERROR] Failed to create UDP listener: %v\n", err)
			return
		}
		defer conn.Close()

		serverUDP, err := net.ResolveUDPAddr("udp", serverAddr)
		if err != nil {
			fmt.Printf("[ERROR] Failed to resolve server address %s: %v\n", serverAddr, err)
			return
		}

		// GUI startup
		appStarted := make(chan bool, 1)
		go func() {
			if err := app.SetRoot(flex, true).Run(); err != nil {
				panic(err)
			}
			appStarted <- true
		}()

		processLineRealtime(logWithTime("INFO", fmt.Sprintf("Connecting to %s", serverAddr)))
		lastSubAck = time.Now()

		// SUB every 0.5x= sec
		go func() {
			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()

			for {
				<-ticker.C
				_, err := conn.WriteToUDP([]byte("SUB"), serverUDP)
				if err != nil {
					processLineRealtime(logWithTime("WARN", fmt.Sprintf("SUB send error: %v", err)))
				}

				if time.Since(lastSubAck) > 1*time.Second {
					if !isConnected {
						if !alreadyWarned {
							processLineRealtime(logWithTime("WARN",
								fmt.Sprintf("Lost connection to %s", serverAddr)))
							alreadyWarned = true
						}
					}
					isConnected = false
				} else {
					if !isConnected {
						processLineRealtime(logWithTime("INFO",
							fmt.Sprintf("Reconnected to %s", serverAddr)))
					}

					isConnected = true
					alreadyWarned = false
				}
			}
		}()

		//  Read UDP real time
		go func() {
			buf := make([]byte, 4096)
			for {
				n, _, err := conn.ReadFromUDP(buf)
				if err != nil {
					if strings.Contains(err.Error(), "use of closed network connection") {
						processLineRealtime("[INFO] UDP connection closed")
						return
					}
					processLineRealtime(fmt.Sprintf("[ERROR] UDP read: %v", err))
					continue
				}

				msg := strings.TrimSpace(string(buf[:n]))

				if strings.HasPrefix(msg, "SUB_ACK") {
					lastSubAck = time.Now()

					if !isConnected {
						isConnected = true
						processLineRealtime(logWithTime("INFO", fmt.Sprintf(
							"Connected. Local: %s, Server: %s",
							conn.LocalAddr().String(), serverAddr,
						)))

					}
					continue
				}

				processLineRealtime(msg)
			}
		}()

	}

	if err := app.SetRoot(flex, true).Run(); err != nil {
		panic(err)
	}
}

func processLine(line string) {
	logEntry := parser.ParseLogLine(line, len(allLogs)+len(logBatch))
	logBatch = append(logBatch, logEntry)

	if len(logBatch) >= batchSize {
		flushLogBatch()
	}
}

func processLineRealtime(line string) {
	logEntry := parser.ParseLogLine(line, len(allLogs))

	app.QueueUpdateDraw(func() {
		allLogs = append(allLogs, logEntry)
		activeLogs++

		if currentFilter != "" {
			searchLower := strings.ToLower(currentFilter)

			if !strings.Contains(strings.ToLower(logEntry.Message), searchLower) &&
				!strings.Contains(strings.ToLower(logEntry.Timestamp), searchLower) &&
				!strings.Contains(strings.ToLower(logEntry.Level), searchLower) &&
				!strings.Contains(strings.ToLower(logEntry.OriginalMessage), searchLower) {
				updateStatusBar(logTable.GetRowCount()-1, currentFilter)
				return
			}

			row := logTable.GetRowCount()

			idxCell := tview.NewTableCell(logEntry.Index)
			timeCell := tview.NewTableCell("")
			levelCell := tview.NewTableCell("")
			msgCell := tview.NewTableCell("")

			highlightSearchText(timeCell, logEntry.Timestamp, currentFilter)
			highlightSearchText(levelCell, logEntry.Level, currentFilter)
			highlightSearchText(msgCell, logEntry.Message, currentFilter)

			levelCell.SetTextColor(logEntry.LevelColor).SetAlign(tview.AlignCenter)

			logTable.SetCell(row, 0, idxCell)
			logTable.SetCell(row, 1, timeCell)
			logTable.SetCell(row, 2, levelCell)
			logTable.SetCell(row, 3, msgCell)

			// add style to row
			setRowStyle(row, idxCell, timeCell, levelCell, msgCell)

			if autoScroll {
				logTable.Select(row, 0)
				logTable.ScrollToEnd()
			}

			displayedCount := logTable.GetRowCount() - 1
			updateStatusBar(displayedCount, currentFilter)
			return
		}

		//  Filter is active
		row := logTable.GetRowCount()

		idxCell := tview.NewTableCell(logEntry.Index)
		timeCell := tview.NewTableCell(logEntry.Timestamp)
		levelCell := tview.NewTableCell(logEntry.Level).
			SetTextColor(logEntry.LevelColor).
			SetAlign(tview.AlignCenter)
		msgCell := tview.NewTableCell(logEntry.Message)

		logTable.SetCell(row, 0, idxCell)
		logTable.SetCell(row, 1, timeCell)
		logTable.SetCell(row, 2, levelCell)
		logTable.SetCell(row, 3, msgCell)

		setRowStyle(row, idxCell, timeCell, levelCell, msgCell)

		updateStatusBar(len(allLogs), "")
		if autoScroll {
			logTable.Select(row, 0)
			logTable.ScrollToEnd()
		}

	})
}

func clearLogs() {
	for i := logTable.GetRowCount() - 1; i > 0; i-- {
		logTable.RemoveRow(i)
	}
	allLogs = make([]model.LogEntry, 0)
	logBatch = make([]model.LogEntry, 0, batchSize)
	activeLogs = 0
	currentFilter = ""
	input.SetText("")

	updateStatusBar(0, "")
}

func flushLogBatch() {
	if len(logBatch) == 0 {
		return
	}

	app.QueueUpdateDraw(func() {
		allLogs = append(allLogs, logBatch...)
		activeLogs += len(logBatch)

		if currentFilter != "" {
			searchLower := strings.ToLower(currentFilter)
			for _, log := range logBatch {
				if strings.Contains(strings.ToLower(log.Message), searchLower) ||
					strings.Contains(strings.ToLower(log.Timestamp), searchLower) ||
					strings.Contains(strings.ToLower(log.Level), searchLower) ||
					strings.Contains(strings.ToLower(log.OriginalMessage), searchLower) {

					row := logTable.GetRowCount()

					idxCell := tview.NewTableCell(log.Index)
					timeCell := tview.NewTableCell("")
					levelCell := tview.NewTableCell("")
					msgCell := tview.NewTableCell("")

					highlightSearchText(timeCell, log.Timestamp, currentFilter)
					highlightSearchText(levelCell, log.Level, currentFilter)
					highlightSearchText(msgCell, log.Message, currentFilter)

					levelCell.SetTextColor(log.LevelColor).SetAlign(tview.AlignCenter)

					logTable.SetCell(row, 0, idxCell)
					logTable.SetCell(row, 1, timeCell)
					logTable.SetCell(row, 2, levelCell)
					logTable.SetCell(row, 3, msgCell)

					setRowStyle(row, idxCell, timeCell, levelCell, msgCell)
				}
			}
			displayedCount := logTable.GetRowCount() - 1
			updateStatusBar(displayedCount, currentFilter)
		} else {
			for _, log := range logBatch {
				row := logTable.GetRowCount()

				idxCell := tview.NewTableCell(log.Index)
				timeCell := tview.NewTableCell(log.Timestamp)
				levelCell := tview.NewTableCell(log.Level).
					SetTextColor(log.LevelColor).
					SetAlign(tview.AlignCenter)
				msgCell := tview.NewTableCell(log.Message)

				logTable.SetCell(row, 0, idxCell)
				logTable.SetCell(row, 1, timeCell)
				logTable.SetCell(row, 2, levelCell)
				logTable.SetCell(row, 3, msgCell)

				setRowStyle(row, idxCell, timeCell, levelCell, msgCell)
			}
			updateStatusBar(len(allLogs), "")
			if autoScroll {
				logTable.ScrollToEnd()
			}

		}

		logBatch = logBatch[:0]
	})
}

func applyFilter(searchText string) {
	for i := logTable.GetRowCount() - 1; i > 0; i-- {
		logTable.RemoveRow(i)
	}

	displayedLogs := 0
	searchLower := strings.ToLower(searchText)

	for _, log := range allLogs {
		if strings.Contains(strings.ToLower(log.Message), searchLower) ||
			strings.Contains(strings.ToLower(log.Timestamp), searchLower) ||
			strings.Contains(strings.ToLower(log.Level), searchLower) ||
			strings.Contains(strings.ToLower(log.OriginalMessage), searchLower) {

			row := logTable.GetRowCount()

			idxCell := tview.NewTableCell(log.Index)
			timeCell := tview.NewTableCell("")
			levelCell := tview.NewTableCell("")
			msgCell := tview.NewTableCell("")

			highlightSearchText(timeCell, log.Timestamp, searchText)
			highlightSearchText(levelCell, log.Level, searchText)
			highlightSearchText(msgCell, log.Message, searchText)

			levelCell.SetTextColor(log.LevelColor).SetAlign(tview.AlignCenter)

			logTable.SetCell(row, 0, idxCell)
			logTable.SetCell(row, 1, timeCell)
			logTable.SetCell(row, 2, levelCell)
			logTable.SetCell(row, 3, msgCell)

			setRowStyle(row, idxCell, timeCell, levelCell, msgCell)

			displayedLogs++
		}
	}

	updateStatusBar(displayedLogs, searchText)

	if displayedLogs > 0 {
		logTable.Select(1, 0)
		logTable.ScrollToBeginning()
	}
}

func clearFilter() {
	currentFilter = ""
	for i := logTable.GetRowCount() - 1; i > 0; i-- {
		logTable.RemoveRow(i)
	}

	for i, log := range allLogs {
		row := logTable.GetRowCount()

		idxCell := tview.NewTableCell(fmt.Sprintf("%d", i))
		timeCell := tview.NewTableCell(log.Timestamp)
		levelCell := tview.NewTableCell(log.Level).
			SetTextColor(log.LevelColor).
			SetAlign(tview.AlignCenter)
		msgCell := tview.NewTableCell(log.Message)

		logTable.SetCell(row, 0, idxCell)
		logTable.SetCell(row, 1, timeCell)
		logTable.SetCell(row, 2, levelCell)
		logTable.SetCell(row, 3, msgCell)

		setRowStyle(row, idxCell, timeCell, levelCell, msgCell)
	}

	updateStatusBar(len(allLogs), "")
}

func highlightSearchText(cell *tview.TableCell, text string, searchText string) {
	if searchText == "" || text == "" {
		cell.SetText(text)
		return
	}

	searchLower := strings.ToLower(searchText)
	textLower := strings.ToLower(text)

	var result strings.Builder
	lastIndex := 0

	for {
		idx := strings.Index(textLower[lastIndex:], searchLower)
		if idx == -1 {
			break
		}

		actualIndex := lastIndex + idx

		result.WriteString(text[lastIndex:actualIndex])

		result.WriteString("[yellow]")
		result.WriteString(text[actualIndex : actualIndex+len(searchText)])
		result.WriteString("[white]")

		lastIndex = actualIndex + len(searchText)
	}

	result.WriteString(text[lastIndex:])

	cell.SetText(result.String())
}

func updateStatusBar(displayed int, filter string) {
	firstLine := statusBar.GetItem(0).(*tview.Flex)
	infoText := firstLine.GetItem(0).(*tview.TextView)

	if filter == "typing..." {
		if inputFile != "" {
			infoText.SetText(fmt.Sprintf(
				"gofly-cli v.%s    Mode: Typing...    %s    Logs: %d",
				appVersion, currentMode, len(allLogs)))
		} else {
			infoText.SetText(fmt.Sprintf(
				"gofly-cli v.%s    Mode: Typing...    [%s]    Logs: %d",
				appVersion, serverAddr, len(allLogs)))
		}
	} else if filter != "" {
		if inputFile != "" {
			infoText.SetText(fmt.Sprintf(
				"gofly-cli v.%s    Mode: Filtered    %s    Logs: %d/%d",
				appVersion, currentMode, displayed, len(allLogs)))
		} else {
			infoText.SetText(fmt.Sprintf(
				"gofly-cli v.%s    Mode: Filtered    [%s]    Logs: %d/%d",
				appVersion, serverAddr, displayed, len(allLogs)))
		}
	} else {
		if inputFile != "" {
			infoText.SetText(fmt.Sprintf(
				"gofly-cli v.%s    Mode: %s    Logs: %d",
				appVersion, currentMode, len(allLogs)))
		} else {
			infoText.SetText(fmt.Sprintf(
				"gofly-cli v.%s    Mode: %s    [%s]    Logs: %d",
				appVersion, currentMode, serverAddr, len(allLogs)))
		}
	}
}

func showHelp() {
	var helpText string
	if inputFile != "" {
		helpText = fmt.Sprintf("Hotkeys:\n\nEsc: Quit/Focus log table\nF1: Help\nF3: Focus filter input\nF4: Clear filter\nF5: Clear all logs\n\nCurrent mode: File [%s]\nSearch works in: Time, Level, Message columns", inputFile)
	} else {
		helpText = fmt.Sprintf("Hotkeys:\n\nEsc: Quit/Focus log table\nF1: Help\nF3: Focus filter input\nF4: Clear filter\nF5: Clear all logs\n\nCurrent mode: Online [%s]\nSearch works in: Time, Level, Message columns\nSUB: Send SUB every 5 sec for updating udp session ttl", serverAddr)
	}

	modal := tview.NewModal().
		SetText(helpText).
		AddButtons([]string{"Close"}).
		SetDoneFunc(func(buttonIndex int, buttonLabel string) {
			app.SetRoot(flex, true).SetFocus(logTable)
		})
	app.SetRoot(modal, true).SetFocus(modal)
}

func extractCallID(message string) string {
	lowerMsg := strings.ToLower(message)
	if idx := strings.Index(lowerMsg, "call-id:"); idx != -1 {
		start := idx + len("call-id: ")
		substr := message[start:]

		end := len(substr)
		for i := 0; i < len(substr); i++ {
			if substr[i] == ' ' {
				end = i
				break
			}
		}

		callID := strings.TrimSpace(substr[:end])
		if callID != "" {
			return callID
		}
	}

	return ""
}

func getSessionColor(callID string) tcell.Color {
	if callID == "" {
		return tcell.ColorGray
	}

	if color, exists := sessionColors[callID]; exists {
		return color
	}

	colorPool := []tcell.Color{
		tcell.NewHexColor(0xFF6B6B), // Красный
		tcell.NewHexColor(0xFFD166), // Желтый
		tcell.NewHexColor(0x06D6A0), // Зеленый
		tcell.NewHexColor(0x118AB2), // Синий
		tcell.NewHexColor(0x9D4EDD), // Фиолетовый
		tcell.NewHexColor(0x00BBF9), // Голубой
		tcell.NewHexColor(0xFF9E6D), // Оранжевый
	}

	color := colorPool[colorIndex%len(colorPool)]
	colorIndex++

	sessionColors[callID] = color

	return color
}

func getSessionRowColor(callID string, row int) tcell.Color {
	sessionColor := getSessionColor(callID)

	r, g, b := sessionColor.RGB()

	baseGray := 45

	mixRatio := 0.3

	mixedR := int(float64(baseGray)*(1-mixRatio) + float64(r)*mixRatio)
	mixedG := int(float64(baseGray)*(1-mixRatio) + float64(g)*mixRatio)
	mixedB := int(float64(baseGray)*(1-mixRatio) + float64(b)*mixRatio)

	if mixedR < 25 {
		mixedR = 25
	}
	if mixedG < 25 {
		mixedG = 25
	}
	if mixedB < 25 {
		mixedB = 25
	}
	if mixedR > 90 {
		mixedR = 90
	}
	if mixedG > 90 {
		mixedG = 90
	}
	if mixedB > 90 {
		mixedB = 90
	}

	return tcell.NewRGBColor(int32(mixedR), int32(mixedG), int32(mixedB))
}

func setRowStyle(row int, cells ...*tview.TableCell) {
	msgCell := cells[3]
	callID := extractCallID(msgCell.Text)

	var bgColor tcell.Color
	var textColor tcell.Color

	if callID != "" {
		bgColor = getSessionRowColor(callID, row)

		sessionColor := getSessionColor(callID)
		r, g, b := sessionColor.RGB()

		lightR := int(float64(r) * 1.5)
		lightG := int(float64(g) * 1.5)
		lightB := int(float64(b) * 1.5)

		if lightR > 255 {
			lightR = 255
		}
		if lightG > 255 {
			lightG = 255
		}
		if lightB > 255 {
			lightB = 255
		}
		if lightR < 180 {
			lightR = 180
		}
		if lightG < 180 {
			lightG = 180
		}
		if lightB < 180 {
			lightB = 180
		}

		textColor = tcell.NewRGBColor(int32(lightR), int32(lightG), int32(lightB))
	} else {
		if row%2 == 0 {
			bgColor = tcell.NewHexColor(0x1E1E1E) // dark gray
		} else {
			bgColor = tcell.NewHexColor(0x2D2D2D) // light gray
		}
		textColor = tcell.ColorWhite
	}

	for _, cell := range cells {
		cell.SetBackgroundColor(bgColor)
		if cell != cells[2] {
			cell.SetTextColor(textColor)
		}
	}
}

func updateHotBar(hotBar *tview.TextView, autoScrollEnabled bool) {
	hotBar.SetDynamicColors(true)

	f6Color := "[red]"
	if autoScrollEnabled {
		f6Color = "[green]"
	}

	text := fmt.Sprintf(
		"[yellow]Esc\\Q[-] Quit   "+
			"[green]F1[-] Help   "+
			"[green]F3[-] Focus Filter   "+
			"[green]F4[-] Clear Filter   "+
			"[green]F5[-] Clear   "+
			"%sF6[-] AutoScroll",
		f6Color,
	)

	hotBar.SetText(text)
}

func logWithTime(level, msg string) string {
	ts := time.Now().Format("2006-01-02 15:04:05")
	return fmt.Sprintf("[%s] [%s] %s", ts, level, msg)
}
