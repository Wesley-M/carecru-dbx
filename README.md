# dbx - Database Query TUI

A fast, lightweight terminal-based UI for querying databases via HTTP API. Built for quick data exploration and analysis with a clean, keyboard-driven interface.

![dbx TUI](https://img.shields.io/badge/interface-TUI-blue)
![Go](https://img.shields.io/badge/language-Go-00ADD8)

## Features

- 🎯 **Multi-line SQL editor** with syntax-friendly input
- 📊 **Smart table rendering** with automatic column width optimization
- 📝 **Query history** with preview and persistent storage
- 🔍 **Detailed row inspection** for exploring individual records
- 📤 **JSON export** with one keystroke (Ctrl-E)
- 🔄 **Column sorting** - click headers to sort ascending/descending
- 🟢 **Live connection status** monitoring
- ⌨️  **Keyboard-first** design with intuitive shortcuts
- 🖱️  **Mouse support** for clicking between panes
- 💾 **Auto-save history** - every query is preserved

## Installation

### Prerequisites
- Go 1.16 or higher
- A database API running on `http://localhost:8000/db?q=<query>`

### Build from source
```bash
git clone https://github.com/Wesley-M/carecru-dbx.git
cd carecru-dbx
go build -o dbx db.go
```

## Usage

### Interactive TUI Mode
Start the interactive terminal interface:
```bash
./dbx
```

### CLI Mode
Execute a single query and output JSON:
```bash
./dbx 'select * from "Patients" limit 10'
```

**Note:** Quote your entire query to prevent shell expansion of special characters like `*`.

## Keyboard Shortcuts

### Query Execution
| Key | Action |
|-----|--------|
| `Enter` | Run query (queries are auto-saved to history) |

### Navigation
| Key | Action |
|-----|--------|
| `Tab` | Cycle through panes |
| `Arrow Keys` | Navigate within panes |

### History
| Key | Action |
|-----|--------|
| `D` | Delete selected history entry |
| `Click/Enter` | Load query into editor |

### Results
| Key | Action |
|-----|--------|
| `Click Header` | Sort by column (toggles asc/desc) |
| `Ctrl-E` | Export results to JSON file |

### Other
| Key | Action |
|-----|--------|
| `Ctrl-Q` | Quit |

**Note on macOS Terminal:** Some keyboard shortcuts like `Shift-Enter` and `Shift-?` don't work reliably in the native Terminal app due to key binding limitations. Use the built-in editor for multi-line queries (just type them normally), and reference this README for help instead of the in-app modal.

## Interface Layout

```
┌─────────────────────────────────────────────────────────┐
│ Connection: ● Connected                                  │
├──────────────┬──────────────────────────────────────────┤
│              │                                           │
│  History     │  Editor                                   │
│  ┌─────────┐ │  [SQL query input]                       │
│  │ Queries │ │                                           │
│  │         │ │  Results (X rows)                        │
│  │         │ │  ┌─────────────────────────────────────┐ │
│  └─────────┘ │  │ id │ name │ email │ created_at │   │ │
│              │  ├────┼──────┼───────┼────────────┤   │ │
│  Preview     │  │ Data rows...                     │   │ │
│  [Selected   │  └─────────────────────────────────────┘ │
│   query]     │                                           │
│              │  Detail │ Raw Output                      │
│              │  [Row]   │ [JSON]                         │
└──────────────┴──────────────────────────────────────────┘
│ Status: Ready                                            │
└─────────────────────────────────────────────────────────┘
```

## Configuration

History is automatically stored in:
- `$XDG_CONFIG_HOME/dbx/history.json`, or
- `~/.config/dbx/history.json`

The last 200 queries are kept by default.

## API Requirements

dbx expects a database API endpoint at `http://localhost:8000/db` that:
- Accepts SQL queries via the `q` parameter: `/db?q=<url-encoded-sql>`
- Returns JSON responses (arrays of objects preferred)
- Falls back gracefully to raw text for non-JSON responses

Example response format:
```json
[
  {
    "id": 1,
    "name": "John Doe",
    "email": "john@example.com"
  },
  {
    "id": 2,
    "name": "Jane Smith",
    "email": "jane@example.com"
  }
]
```

## Features in Detail

### Smart Column Display
- Columns are sorted alphabetically for consistency
- Column widths auto-adjust based on content (max 40 chars)
- Long values are truncated with ellipsis (…)
- Full values viewable in the Detail pane

### Result Sorting
Click any column header (or navigate with arrows and press Enter) to sort results. Click again to reverse the sort order. The title shows which column is sorted with an up (↑) or down (↓) arrow.

### Export
Press `Ctrl-E` to export current results to a timestamped JSON file:
```
dbx_export_1701388800.json
```

### Connection Monitoring
The connection status indicator checks the API every 5 seconds:
- 🟢 **Connected** - API is responding
- 🟡 **Server Error** - API returned 5xx error
- 🔴 **Disconnected** - Cannot reach API

## Tips

- Multi-line queries work automatically - just type your SQL across multiple lines before pressing Enter
- History entries show timestamp and full query text on hover
- The Detail pane is great for inspecting long text fields or JSON columns
- Raw Output shows the exact API response for debugging
- Export feature is perfect for sharing query results with teammates
- All queries are automatically saved to history when executed

## Troubleshooting

**Connection refused error:**
- Ensure your database API is running on `localhost:8000`
- Check that the `/db` endpoint accepts the `q` parameter

**Query not executing:**
- Make sure you're focused on the editor (press Tab to cycle focus)
- Check the status bar for error messages

**History not saving:**
- Verify write permissions to `~/.config/dbx/`
- Check disk space

## License

MIT

## Contributing

Contributions welcome! Please feel free to submit issues or pull requests.
