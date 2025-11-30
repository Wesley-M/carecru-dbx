package main

// db-tui.go
// A single-file TUI for querying a local DB API and browsing results.
// - Uses tview for terminal UI
// - Calls local API: http://localhost:8000/db?q=<url-escaped-sql>
// - Persists history to $XDG_CONFIG_HOME/dbx/history.json (or ~/.config/dbx/history.json)
// - Best-effort JSON parsing of results; falls back to raw text

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

const (
	defaultAPI = "http://localhost:8000/db?q="
)

// Config holds application configuration
type Config struct {
	ScrollAcceleration    int `json:"scroll_acceleration"`     // Rows to skip when holding arrow keys
	ScrollRepeatThreshold int `json:"scroll_repeat_threshold"` // Number of repeats before acceleration kicks in
	ScrollRepeatTimeoutMs int `json:"scroll_repeat_timeout_ms"` // Milliseconds to detect key repeat
	PageScrollStep        int `json:"page_scroll_step"`        // Rows to jump for Page Up/Down
	MaxHistoryEntries     int `json:"max_history_entries"`     // Maximum number of history entries to keep
	ConnectionCheckSec    int `json:"connection_check_sec"`    // Seconds between connection status checks
	MaxColumnWidth        int `json:"max_column_width"`        // Maximum width for table columns
}

// DefaultConfig returns the default configuration
func DefaultConfig() Config {
	return Config{
		ScrollAcceleration:    3,
		ScrollRepeatThreshold: 3,
		ScrollRepeatTimeoutMs: 150,
		PageScrollStep:        10,
		MaxHistoryEntries:     200,
		ConnectionCheckSec:    5,
		MaxColumnWidth:        40,
	}
}

func configPath() (string, error) {
	if env := os.Getenv("XDG_CONFIG_HOME"); env != "" {
		return filepath.Join(env, "dbx", "config.json"), nil
	}
	usr, err := user.Current()
	if err != nil {
		return "", err
	}
	return filepath.Join(usr.HomeDir, ".config", "dbx", "config.json"), nil
}

func loadConfig() (*Config, error) {
	p, err := configPath()
	if err != nil {
		cfg := DefaultConfig()
		return &cfg, nil
	}
	b, err := ioutil.ReadFile(p)
	if os.IsNotExist(err) {
		// Create default config file
		cfg := DefaultConfig()
		saveConfig(&cfg)
		return &cfg, nil
	}
	if err != nil {
		cfg := DefaultConfig()
		return &cfg, nil
	}
	var cfg Config
	if err := json.Unmarshal(b, &cfg); err != nil {
		// If corrupted, return default config
		defCfg := DefaultConfig()
		return &defCfg, nil
	}
	return &cfg, nil
}

func saveConfig(cfg *Config) error {
	p, err := configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return ioutil.WriteFile(p, b, 0o644)
}

// HistoryEntry stores a query and timestamp
type HistoryEntry struct {
	Query     string    `json:"query"`
	Timestamp time.Time `json:"timestamp"`
}

// History holds recent queries
type History struct {
	Entries []HistoryEntry `json:"entries"`
}

func historyPath() (string, error) {
	if env := os.Getenv("XDG_CONFIG_HOME"); env != "" {
		return filepath.Join(env, "dbx", "history.json"), nil
	}
	usr, err := user.Current()
	if err != nil {
		return "", err
	}
	return filepath.Join(usr.HomeDir, ".config", "dbx", "history.json"), nil
}

func loadHistory() (*History, error) {
	p, err := historyPath()
	if err != nil {
		return nil, err
	}
	b, err := ioutil.ReadFile(p)
	if os.IsNotExist(err) {
		return &History{Entries: []HistoryEntry{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var h History
	if err := json.Unmarshal(b, &h); err != nil {
		// if corrupted, return empty history rather than fail the app
		return &History{Entries: []HistoryEntry{}}, nil
	}
	return &h, nil
}

func saveHistory(h *History) error {
	p, err := historyPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return err
	}
	return ioutil.WriteFile(p, b, 0o644)
}

// appendHistory appends a query to history, keeping maxLen entries
func appendHistory(h *History, query string, maxLen int) {
	query = strings.TrimSpace(query)
	if query == "" {
		return
	}
	// avoid consecutive duplicates
	if len(h.Entries) > 0 && h.Entries[0].Query == query {
		// touch timestamp
		h.Entries[0].Timestamp = time.Now()
		return
	}
	h.Entries = append([]HistoryEntry{{Query: query, Timestamp: time.Now()}}, h.Entries...)
	if len(h.Entries) > maxLen {
		h.Entries = h.Entries[:maxLen]
	}
}

// fetchQuery runs the query against local API and returns parsed data, type, raw response, and error
func fetchQuery(apiBase, query string) (interface{}, string, string, error) {
	encoded := url.QueryEscape(query)
	full := apiBase + encoded
	resp, err := http.Get(full)
	if err != nil {
		return nil, "", "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", "", err
	}
	raw := string(b)
	// try parse JSON
	var arr []map[string]interface{}
	if err := json.Unmarshal(b, &arr); err == nil {
		return arr, "json", raw, nil
	}
	// try parse generic JSON
	var gen interface{}
	if err := json.Unmarshal(b, &gen); err == nil {
		return gen, "json", raw, nil
	}
	// fallback to raw text
	return raw, "text", raw, nil
}

// truncateString truncates a string to maxLen and adds ellipsis if needed
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-1] + "…"
}

// renderJSONToTable converts a slice of maps into columns and rows with smart column widths
func renderJSONToTable(data []map[string]interface{}, table *tview.Table, columns *[]string, cfg *Config) {
	table.Clear()
	if len(data) == 0 {
		return
	}
	// collect column order by first object's keys
	cols := make([]string, 0, len(data[0]))
	for k := range data[0] {
		cols = append(cols, k)
	}
	// Sort columns alphabetically
	sort.Strings(cols)
	*columns = cols
	
	// Calculate max widths for each column (limit to reasonable sizes)
	maxColWidth := cfg.MaxColumnWidth
	const minColWidth = 8
	colWidths := make(map[string]int)
	for _, k := range cols {
		// Start with header width
		width := len(k)
		if width < minColWidth {
			width = minColWidth
		}
		// Check first few rows to determine good width
		for i := 0; i < len(data) && i < 5; i++ {
			val := fmt.Sprintf("%v", data[i][k])
			if len(val) > width {
				width = len(val)
			}
		}
		if width > maxColWidth {
			width = maxColWidth
		}
		colWidths[k] = width
	}
	
	// header - make clickable for sorting
	for c, k := range cols {
		cell := tview.NewTableCell(k).SetSelectable(true).SetAttributes(tcell.AttrBold).SetMaxWidth(colWidths[k])
		table.SetCell(0, c, cell)
	}
	// rows
	for r, row := range data {
		for c, k := range cols {
			val := row[k]
			s := fmt.Sprintf("%v", val)
			// Truncate if needed
			if len(s) > colWidths[k] {
				s = truncateString(s, colWidths[k])
			}
			cell := tview.NewTableCell(s).SetMaxWidth(colWidths[k])
			table.SetCell(r+1, c, cell)
		}
	}
}

func main() {
	// Load configuration
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to load config, using defaults: %v\n", err)
		defCfg := DefaultConfig()
		cfg = &defCfg
	}

	// Check for command-line query argument
	if len(os.Args) > 1 {
		// Show usage hint if no valid query detected
		if len(os.Args) == 2 && (os.Args[1] == "-h" || os.Args[1] == "--help" || os.Args[1] == "help") {
			fmt.Println("Usage:")
			fmt.Println("  dbx                    Start interactive TUI")
			fmt.Println("  dbx 'QUERY'            Execute query and output JSON")
			fmt.Println("")
			fmt.Println("Examples:")
			fmt.Println("  dbx 'select * from Patients limit 1'")
			fmt.Println("  dbx 'select count(*) from Users'")
			fmt.Println("")
			fmt.Println("Note: Quote the entire query to prevent shell expansion of * and other special characters")
			return
		}
		
		query := strings.Join(os.Args[1:], " ")
		data, dataType, raw, err := fetchQuery(defaultAPI, query)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		
		// Output based on data type
		if dataType == "json" {
			// Pretty print JSON
			if arr, ok := data.([]map[string]interface{}); ok && len(arr) > 0 {
				b, err := json.MarshalIndent(arr, "", "  ")
				if err != nil {
					fmt.Println(raw)
				} else {
					fmt.Println(string(b))
				}
			} else {
				b, err := json.MarshalIndent(data, "", "  ")
				if err != nil {
					fmt.Println(raw)
				} else {
					fmt.Println(string(b))
				}
			}
		} else {
			fmt.Println(raw)
		}
		return
	}

	// No arguments - start TUI
	app := tview.NewApplication()

	// UI components
	historyList := tview.NewList().ShowSecondaryText(false)
	historyList.SetBorder(true).SetTitle("History")
	
	// Declare updateFocusColors early so we can use it in mouse handlers
	var updateFocusColors func(tview.Primitive)

	historyPreview := tview.NewTextView().SetDynamicColors(true).SetScrollable(true).SetWordWrap(true)
	historyPreview.SetBorder(true).SetTitle("Preview")

	queryInput := tview.NewTextView()
	queryInput.SetBorder(true).SetTitle("Query (Press Ctrl-R to run, Ctrl-S to save to history)")
	queryInput.SetDynamicColors(true).SetRegions(true).SetWordWrap(true)
	// use an input capture on the page to accept typed text into the query area

	editor := tview.NewTextArea()
	editor.SetPlaceholder("Enter SQL, press Enter to run")
	editor.SetBorder(true).SetTitle("Editor")

	resultsTable := tview.NewTable().SetFixed(1, 0).SetSelectable(true, true)
	resultsTable.SetBorder(true).SetTitle("Results")
	
	// Variables for tracking key repeat for faster scrolling
	var lastKeyTime time.Time
	var lastKey tcell.Key
	var keyRepeatCount int
	
	// Add faster scrolling for results table with acceleration
	resultsTable.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		row, col := resultsTable.GetSelection()
		rowCount := resultsTable.GetRowCount()
		
		now := time.Now()
		
		switch event.Key() {
		case tcell.KeyPgDn:
			// Jump down by configured page step
			newRow := row + cfg.PageScrollStep
			if newRow >= rowCount {
				newRow = rowCount - 1
			}
			resultsTable.Select(newRow, col)
			return nil
		case tcell.KeyPgUp:
			// Jump up by configured page step
			newRow := row - cfg.PageScrollStep
			if newRow < 1 { // Skip header row (row 0)
				newRow = 1
			}
			resultsTable.Select(newRow, col)
			return nil
		case tcell.KeyDown, tcell.KeyUp:
			// Detect key repeat: if same key pressed within configured timeout, it's a repeat
			isRepeat := false
			if event.Key() == lastKey && now.Sub(lastKeyTime) < time.Duration(cfg.ScrollRepeatTimeoutMs)*time.Millisecond {
				keyRepeatCount++
				isRepeat = true
			} else {
				keyRepeatCount = 0
			}
			lastKey = event.Key()
			lastKeyTime = now
			
			// Calculate scroll step: start with 1, accelerate after threshold
			step := 1
			if isRepeat && keyRepeatCount > cfg.ScrollRepeatThreshold {
				step = cfg.ScrollAcceleration
			}
			
			var newRow int
			if event.Key() == tcell.KeyDown {
				newRow = row + step
				if newRow >= rowCount {
					newRow = rowCount - 1
				}
			} else {
				newRow = row - step
				if newRow < 1 {
					newRow = 1
				}
			}
			resultsTable.Select(newRow, col)
			return nil
		}
		return event
	})

	detailView := tview.NewTextView().SetDynamicColors(true).SetScrollable(true).SetWordWrap(true)
	detailView.SetBorder(true).SetTitle("Detail")

	rawView := tview.NewTextView().SetDynamicColors(true).SetScrollable(true)
	rawView.SetBorder(true).SetTitle("Raw Output")

	connectionStatus := tview.NewTextView().SetDynamicColors(true)
	connectionStatus.SetBorder(true).SetTitle("Connection")
	connectionStatus.SetText("[yellow]●[white] Checking...")

	status := tview.NewTextView().SetDynamicColors(true)
	status.SetBorder(false)
	
	// Function to update border colors based on focus
	updateFocusColors = func(focused tview.Primitive) {
		// Reset all borders to default color
		historyList.SetBorderColor(tcell.ColorWhite)
		editor.SetBorderColor(tcell.ColorWhite)
		resultsTable.SetBorderColor(tcell.ColorWhite)
		detailView.SetBorderColor(tcell.ColorWhite)
		rawView.SetBorderColor(tcell.ColorWhite)
		
		// Highlight focused pane with green
		switch focused {
		case historyList:
			historyList.SetBorderColor(tcell.ColorGreen)
		case editor:
			editor.SetBorderColor(tcell.ColorGreen)
		case resultsTable:
			resultsTable.SetBorderColor(tcell.ColorGreen)
		case detailView:
			detailView.SetBorderColor(tcell.ColorGreen)
		case rawView:
			rawView.SetBorderColor(tcell.ColorGreen)
		}
	}
	
	// Add mouse click handlers to update focus colors
	historyList.SetMouseCapture(func(action tview.MouseAction, event *tcell.EventMouse) (tview.MouseAction, *tcell.EventMouse) {
		if action == tview.MouseLeftClick {
			updateFocusColors(historyList)
		}
		return action, event
	})
	editor.SetMouseCapture(func(action tview.MouseAction, event *tcell.EventMouse) (tview.MouseAction, *tcell.EventMouse) {
		if action == tview.MouseLeftClick {
			updateFocusColors(editor)
		}
		return action, event
	})
	resultsTable.SetMouseCapture(func(action tview.MouseAction, event *tcell.EventMouse) (tview.MouseAction, *tcell.EventMouse) {
		if action == tview.MouseLeftClick {
			updateFocusColors(resultsTable)
		}
		return action, event
	})
	detailView.SetMouseCapture(func(action tview.MouseAction, event *tcell.EventMouse) (tview.MouseAction, *tcell.EventMouse) {
		if action == tview.MouseLeftClick {
			updateFocusColors(detailView)
		}
		return action, event
	})
	rawView.SetMouseCapture(func(action tview.MouseAction, event *tcell.EventMouse) (tview.MouseAction, *tcell.EventMouse) {
		if action == tview.MouseLeftClick {
			updateFocusColors(rawView)
		}
		return action, event
	})

	// layout
	flex := tview.NewFlex().SetDirection(tview.FlexRow)
	
	// Top bar with connection status
	topBar := tview.NewFlex()
	topBar.AddItem(connectionStatus, 20, 0, false)
	
	flex.AddItem(topBar, 3, 0, false)
	
	top := tview.NewFlex()
	
	// History column with list and preview
	historyColumn := tview.NewFlex().SetDirection(tview.FlexRow)
	historyColumn.AddItem(historyList, 0, 2, false)
	historyColumn.AddItem(historyPreview, 0, 1, false)
	
	top.AddItem(historyColumn, 30, 1, false)

	bottomRow := tview.NewFlex()
	bottomRow.AddItem(detailView, 0, 1, true)
	bottomRow.AddItem(rawView, 0, 1, true)

	center := tview.NewFlex().SetDirection(tview.FlexRow)
	center.AddItem(editor, 5, 0, true)
	center.AddItem(resultsTable, 0, 2, false)
	center.AddItem(bottomRow, 0, 1, true)

	top.AddItem(center, 0, 3, true)

	flex.AddItem(top, 0, 1, true)
	flex.AddItem(status, 1, 0, false)

	// history loading
	hist, err := loadHistory()
	if err != nil {
		// ignore errors but show in status
		status.SetText(fmt.Sprintf("[red]Failed to load history: %v", err))
		hist = &History{Entries: []HistoryEntry{}}
	}

	refreshHistoryList := func() {
		historyList.Clear()
		for i, e := range hist.Entries {
			label := fmt.Sprintf("%s — %s", e.Timestamp.Format("2006-01-02 15:04:05"), e.Query)
			// capture index
			idx := i
			historyList.AddItem(label, "", 0, func() {
				editor.SetText(hist.Entries[idx].Query, true)
				app.SetFocus(editor)
				updateFocusColors(editor)
			})
			if i >= 100 {
				break
			}
		}
	}

	// Update history preview when selection changes
	historyList.SetChangedFunc(func(index int, mainText string, secondaryText string, shortcut rune) {
		if index >= 0 && index < len(hist.Entries) {
			entry := hist.Entries[index]
			var preview strings.Builder
			preview.WriteString(fmt.Sprintf("[yellow]Time:[white] %s\n\n", entry.Timestamp.Format("2006-01-02 15:04:05")))
			preview.WriteString("[yellow]Query:[white]\n")
			preview.WriteString(entry.Query)
			historyPreview.SetText(preview.String())
			historyPreview.ScrollToBeginning()
		} else {
			historyPreview.SetText("[gray]No history selected")
		}
	})

	refreshHistoryList()

	// Show first history item in preview if available
	if len(hist.Entries) > 0 {
		var preview strings.Builder
		preview.WriteString(fmt.Sprintf("[yellow]Time:[white] %s\n\n", hist.Entries[0].Timestamp.Format("2006-01-02 15:04:05")))
		preview.WriteString("[yellow]Query:[white]\n")
		preview.WriteString(hist.Entries[0].Query)
		historyPreview.SetText(preview.String())
	} else {
		historyPreview.SetText("[gray]No history available")
	}

	// helper to set status message
	setStatus := func(format string, a ...interface{}) {
		status.SetText(fmt.Sprintf(format, a...))
	}

	// Add input handler for history list to delete entries with 'd'
	historyList.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Rune() == 'd' || event.Rune() == 'D' {
			currentItem := historyList.GetCurrentItem()
			if currentItem >= 0 && currentItem < len(hist.Entries) {
				// Remove the entry from history
				hist.Entries = append(hist.Entries[:currentItem], hist.Entries[currentItem+1:]...)
				// Save updated history
				if err := saveHistory(hist); err != nil {
					setStatus("[red]Failed to save history: %v", err)
				} else {
					// Refresh the list
					refreshHistoryList()
					// Try to select the same position or the last item
					newCount := historyList.GetItemCount()
					if newCount > 0 {
						if currentItem >= newCount {
							historyList.SetCurrentItem(newCount - 1)
						} else {
							historyList.SetCurrentItem(currentItem)
						}
					} else {
						historyPreview.SetText("[gray]No history")
					}
					setStatus("[green]History entry deleted")
				}
			}
			return nil
		}
		return event
	})

	currentRowCount := 0
	var currentData []map[string]interface{}
	var currentColumns []string
	sortColumn := -1
	sortAscending := true

	// Function to update detail view based on selected row
	updateDetailView := func() {
		row, _ := resultsTable.GetSelection()
		if row <= 0 || row > len(currentData) {
			detailView.SetText("[yellow]No row selected")
			return
		}
		rowData := currentData[row-1]
		var details strings.Builder
		details.WriteString(fmt.Sprintf("[yellow::b]Row %d/%d[white]\n", row, len(currentData)))
		
		// Get keys in sorted order for consistent display
		keys := make([]string, 0, len(rowData))
		for k := range rowData {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		
		for _, k := range keys {
			v := rowData[k]
			valStr := fmt.Sprintf("%v", v)
			// Compact display: field: value
			if len(valStr) > 200 {
				valStr = valStr[:200] + "…"
			}
			details.WriteString(fmt.Sprintf("[yellow]%s:[white] %s\n", k, valStr))
		}
		detailView.SetText(details.String())
		detailView.ScrollToBeginning()
	}

	// Setup selection changed handler for results table
	resultsTable.SetSelectionChangedFunc(func(row, col int) {
		if row > 0 && len(currentData) > 0 {
			updateDetailView()
		}
	})

	// Setup click handler for column sorting
	resultsTable.SetSelectedFunc(func(row, col int) {
		if row == 0 && len(currentData) > 0 && col < len(currentColumns) {
			// Clicked on header - sort by this column
			colName := currentColumns[col]
			
			// Toggle sort direction if same column
			if sortColumn == col {
				sortAscending = !sortAscending
			} else {
				sortColumn = col
				sortAscending = true
			}
			
			// Sort the data
			sort.Slice(currentData, func(i, j int) bool {
				vi := fmt.Sprintf("%v", currentData[i][colName])
				vj := fmt.Sprintf("%v", currentData[j][colName])
				if sortAscending {
					return vi < vj
				}
				return vi > vj
			})
			
			// Re-render table
			renderJSONToTable(currentData, resultsTable, &currentColumns, cfg)
			resultsTable.SetTitle(fmt.Sprintf("Results (%d rows) [sorted by %s %s]", len(currentData), colName, map[bool]string{true: "↑", false: "↓"}[sortAscending]))
			resultsTable.Select(1, 0)
			updateDetailView()
		}
	})

	runQuery := func(query string) {
		setStatus("[yellow]Running query...")
		sortColumn = -1 // Reset sorting
		sortAscending = true

		// Auto-save to history
		appendHistory(hist, query, cfg.MaxHistoryEntries)
		if err := saveHistory(hist); err != nil {
			setStatus("[red]Failed to save history: %v", err)
		} else {
			refreshHistoryList()
		}

		// Focus results table immediately
		app.SetFocus(resultsTable)
		currentRowCount = 0
		resultsTable.Clear()

		go func() {
			res, kind, raw, err := fetchQuery(defaultAPI, query)
			
			app.QueueUpdateDraw(func() {
				if err != nil {
					setStatus("[red]Error: %v", err)
					rawView.SetText(fmt.Sprintf("Error: %v", err))
					rawView.ScrollToBeginning()
					return
				}
				
				// Always show raw output
				rawView.SetText(raw)
				rawView.ScrollToBeginning()

				if kind == "json" {
					// try cast to []map[string]interface{}
					switch v := res.(type) {
				case []map[string]interface{}:
					currentData = v
					renderJSONToTable(v, resultsTable, &currentColumns, cfg)
					currentRowCount = len(v)
					resultsTable.SetTitle(fmt.Sprintf("Results (%d rows)", currentRowCount))
					if currentRowCount > 0 {
						resultsTable.Select(1, 0)
						updateDetailView()
					} else {
						detailView.SetText("[yellow]No results")
					}
					setStatus("[green]Fetched %d rows", len(v))
					return
					case []interface{}:
						// try convert items to map
						maps := make([]map[string]interface{}, 0, len(v))
						for _, item := range v {
							if m, ok := item.(map[string]interface{}); ok {
								maps = append(maps, m)
							} else {
								// not uniform, table might be empty or partial
							}
						}
					if len(maps) > 0 {
						currentData = maps
						renderJSONToTable(maps, resultsTable, &currentColumns, cfg)
						currentRowCount = len(maps)
						resultsTable.SetTitle(fmt.Sprintf("Results (%d rows)", currentRowCount))
						resultsTable.Select(1, 0)
						updateDetailView()
						setStatus("[green]Fetched %d rows", len(maps))
					} else {
						resultsTable.Clear()
						currentData = nil
						currentRowCount = 0
						detailView.SetText("[yellow]JSON result (non-tabular)")
						setStatus("[green]JSON result (non-tabular)")
					}
						return
					default:
						resultsTable.Clear()
						currentData = nil
						currentRowCount = 0
						detailView.SetText("[yellow]JSON result (see raw output)")
						setStatus("[green]JSON result")
						return
					}
			}
			// text
			resultsTable.Clear()
			currentData = nil
			currentRowCount = 0
			detailView.SetText("[yellow]Text result (see raw output)")
			setStatus("[green]Text result")
		})
	}()
	}

	// Connection status checker
	go func() {
		for {
			resp, err := http.Get(defaultAPI[:len(defaultAPI)-3]) // Remove "?q=" suffix
			app.QueueUpdateDraw(func() {
				if err == nil && resp != nil {
					resp.Body.Close()
					if resp.StatusCode < 500 {
						connectionStatus.SetText("[green]●[white] Connected")
					} else {
						connectionStatus.SetText("[yellow]●[white] Server Error")
					}
				} else {
					connectionStatus.SetText("[red]●[white] Disconnected")
				}
			})
			time.Sleep(time.Duration(cfg.ConnectionCheckSec) * time.Second)
		}
	}()

	// keybindings
	app.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		// Ctrl-E to export results
		if ev.Modifiers() == tcell.ModCtrl && ev.Rune() == 'e' {
			if len(currentData) > 0 {
				b, err := json.MarshalIndent(currentData, "", "  ")
				if err == nil {
					filename := fmt.Sprintf("dbx_export_%d.json", time.Now().Unix())
					if err := os.WriteFile(filename, b, 0644); err != nil {
						setStatus("[red]Failed to export: %v", err)
					} else {
						setStatus("[green]Exported %d rows to %s", len(currentData), filename)
					}
				} else {
					setStatus("[red]Failed to marshal JSON: %v", err)
				}
			} else {
				setStatus("[yellow]No results to export")
			}
			return nil
		}

		// Tab to cycle focus
		if ev.Key() == tcell.KeyTab {
			var nextFocus tview.Primitive
			switch app.GetFocus() {
			case editor:
				nextFocus = historyList
			case historyList:
				nextFocus = resultsTable
			case resultsTable:
				nextFocus = detailView
			case detailView:
				nextFocus = rawView
			default:
				nextFocus = editor
			}
			app.SetFocus(nextFocus)
			updateFocusColors(nextFocus)
			return nil
		}

		// Enter to run query from editor (auto-saves to history)
		if ev.Key() == tcell.KeyEnter && app.GetFocus() == editor {
			q := editor.GetText()
			runQuery(q)
			return nil
		}
		// Ctrl-Q to quit
		if ev.Modifiers() == tcell.ModCtrl && ev.Rune() == 'q' {
			app.Stop()
			return nil
		}
		return ev
	})

	// small help text
	help := "[yellow]Shortcuts:[white] Enter Run  Tab Cycle  D Delete  Ctrl-E Export  Ctrl-Q Quit"
	setStatus(help)

	// start app
	app.SetFocus(editor)
	updateFocusColors(editor)
	if err := app.SetRoot(flex, true).EnableMouse(true).Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error running app: %v\n", err)
		os.Exit(1)
	}
}
