package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/mattn/go-sqlite3"
)

var (
	baseStyle = lipgloss.NewStyle().
			BorderStyle(lipgloss.NormalBorder()).
			BorderForeground(lipgloss.Color("240"))
	errorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("5"))
	focusStyle = lipgloss.NewStyle().BorderForeground(lipgloss.Color("170")).BorderStyle(lipgloss.ThickBorder())
	itemStyle  = lipgloss.NewStyle().PaddingLeft(2)
	selItemStyle = lipgloss.NewStyle().PaddingLeft(2).Foreground(lipgloss.Color("170"))
)

type sessionState int

const (
	stateSelectDB sessionState = iota
	stateInputHost
	stateInputPort
	stateInputUser
	stateInputPass
	stateInputSQLitePath
	stateExplorer
	stateQuery
	stateResult
)

type explorerFocus int

const (
	focusDB explorerFocus = iota
	focusTable
)

type item string

func (i item) FilterValue() string { return string(i) }

type itemDelegate struct{}

func (d itemDelegate) Height() int                               { return 1 }
func (d itemDelegate) Spacing() int                              { return 0 }
func (d itemDelegate) Update(msg tea.Msg, m *list.Model) tea.Cmd { return nil }
func (d itemDelegate) Render(w io.Writer, m list.Model, index int, listItem list.Item) {
	i, ok := listItem.(item)
	if !ok {
		return
	}
	str := string(i)
	if index == m.Index() {
		fmt.Fprint(w, selItemStyle.Render("> "+str))
	} else {
		fmt.Fprint(w, itemStyle.Render("  "+str))
	}
}

type model struct {
	state      sessionState
	db         *sql.DB
	dbType     string
	inputs     []textinput.Model
	dbList     list.Model
	tableList  list.Model
	resTable   table.Model
	schemaView viewport.Model
	jsonView   viewport.Model
	err        error
	width      int
	height     int

	// Explorer state
	focus    explorerFocus
	activeDB string
	results  []map[string]interface{}
	viewMode string // "table" or "json"

	// Connection details
	host, port, user, password, sqlitePath string
}

func initialModel() model {
	dDelegate := itemDelegate{}
	dbList := list.New([]list.Item{}, dDelegate, 0, 0)
	dbList.Title = "Databases"
	dbList.SetShowStatusBar(false)
	dbList.SetFilteringEnabled(false)

	tList := list.New([]list.Item{}, dDelegate, 0, 0)
	tList.Title = "Tables"
	tList.SetShowStatusBar(false)
	tList.SetFilteringEnabled(false)

	return model{
		state:     stateSelectDB,
		dbType:    "mysql",
		dbList:    dbList,
		tableList: tList,
		viewMode:  "table",
	}
}

func (m model) Init() tea.Cmd {
	return textinput.Blink
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "ctrl+q":
			return m, tea.Quit
		case "esc":
			switch m.state {
			case stateResult:
				m.state = stateQuery
				return m, nil
			case stateQuery:
				m.state = stateExplorer
				return m, nil
			case stateExplorer:
				m.state = stateSelectDB
				if m.db != nil {
					m.db.Close()
				}
				return m, nil
			default:
				m.state = stateSelectDB
				return m, nil
			}

		case "up", "down":
			if m.state == stateSelectDB {
				if m.dbType == "mysql" {
					m.dbType = "sqlite3"
				} else {
					m.dbType = "mysql"
				}
				return m, nil
			}

		case "tab":
			if m.state == stateExplorer {
				if m.focus == focusDB {
					m.focus = focusTable
				} else {
					m.focus = focusDB
				}
				return m, nil
			}
			if m.state == stateResult {
				if m.viewMode == "table" {
					m.viewMode = "json"
				} else {
					m.viewMode = "table"
				}
				return m, nil
			}

		case "enter":
			return m.handleEnter()
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.dbList.SetSize(m.width/4, m.height-12)
		m.tableList.SetSize(m.width/4, m.height-12)
		m.schemaView = viewport.New(m.width/2-4, m.height-12)
		m.jsonView = viewport.New(m.width-4, m.height-12)
	}

	switch m.state {
	case stateExplorer:
		if m.focus == focusDB {
			m.dbList, cmd = m.dbList.Update(msg)
			if i, ok := m.dbList.SelectedItem().(item); ok {
				newDB := string(i)
				if newDB != m.activeDB {
					m.activeDB = newDB
					m.refreshTables()
					m.tableList.Select(0)
				}
			}
		} else {
			m.tableList, cmd = m.tableList.Update(msg)
			m.updateSchemaView()
		}

	case stateResult:
		if m.viewMode == "table" {
			m.resTable, cmd = m.resTable.Update(msg)
		} else {
			m.jsonView, cmd = m.jsonView.Update(msg)
		}
	case stateQuery:
		m.inputs[0], cmd = m.inputs[0].Update(msg)
	default:
		if len(m.inputs) > 0 {
			m.inputs[0], cmd = m.inputs[0].Update(msg)
		}
	}

	return m, cmd
}

func (m *model) setupInput(placeholder string, value string, sensitive bool) {
	ti := textinput.New()
	ti.Placeholder = placeholder
	ti.SetValue(value)
	ti.Focus()
	if sensitive {
		ti.EchoMode = textinput.EchoPassword
		ti.EchoCharacter = '*'
	}
	m.inputs = []textinput.Model{ti}
}

func (m model) handleEnter() (model, tea.Cmd) {
	switch m.state {
	case stateSelectDB:
		if m.dbType == "mysql" {
			m.state = stateInputHost
			m.setupInput("Host (localhost)", "localhost", false)
		} else {
			m.state = stateInputSQLitePath
			m.setupInput("Path to SQLite file (test.db)", "test.db", false)
		}
	case stateInputHost:
		m.host = m.inputs[0].Value()
		m.state = stateInputPort
		m.setupInput("Port (3306)", "3306", false)
	case stateInputPort:
		m.port = m.inputs[0].Value()
		m.state = stateInputUser
		m.setupInput("User (root)", "root", false)
	case stateInputUser:
		m.user = m.inputs[0].Value()
		m.state = stateInputPass
		m.setupInput("Password", "", true)
	case stateInputPass:
		m.password = m.inputs[0].Value()
		return m.connect()
	case stateInputSQLitePath:
		m.sqlitePath = m.inputs[0].Value()
		return m.connect()
	case stateExplorer:
		if i, ok := m.tableList.SelectedItem().(item); ok {
			tableName := string(i)
			query := fmt.Sprintf("SELECT * FROM %s LIMIT 50", tableName)
			if m.dbType == "mysql" {
				query = fmt.Sprintf("SELECT * FROM %s.%s LIMIT 50", m.activeDB, tableName)
			}
			m.state = stateQuery
			m.setupInput("Query", query, false)
		}
	case stateQuery:
		return m.handleQuery()
	}
	return m, nil
}

func (m model) connect() (model, tea.Cmd) {
	var driver, dsn string
	if m.dbType == "mysql" {
		driver = "mysql"
		dsn = fmt.Sprintf("%s:%s@tcp(%s:%s)/", m.user, m.password, m.host, m.port)
	} else {
		driver = "sqlite3"
		dsn = m.sqlitePath
	}

	db, err := sql.Open(driver, dsn)
	if err != nil {
		m.err = err
		return m, nil
	}

	if err := db.Ping(); err != nil {
		m.err = err
		db.Close()
		return m, nil
	}

	m.db = db
	m.state = stateExplorer
	m.focus = focusDB
	return m.refreshExplorer()
}

func (m *model) refreshExplorer() (model, tea.Cmd) {
	if m.dbType == "mysql" {
		rows, err := m.db.Query("SHOW DATABASES")
		if err != nil {
			m.err = err
			return *m, nil
		}
		defer rows.Close()
		var dbs []list.Item
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err == nil {
				dbs = append(dbs, item(name))
			}
		}
		m.dbList.SetItems(dbs)
		if len(dbs) > 0 {
			m.activeDB = string(dbs[0].(item))
			m.refreshTables()
		}
	} else {
		m.activeDB = "main"
		m.dbList.SetItems([]list.Item{item("main")})
		m.refreshTables()
	}
	return *m, nil
}

func (m *model) refreshTables() {
	var query string
	if m.dbType == "mysql" {
		query = fmt.Sprintf("SHOW TABLES FROM %s", m.activeDB)
	} else {
		query = "SELECT name FROM sqlite_master WHERE type='table'"
	}

	rows, err := m.db.Query(query)
	if err != nil {
		m.err = err
		return
	}
	defer rows.Close()

	var tables []list.Item
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err == nil {
			tables = append(tables, item(name))
		}
	}
	m.tableList.SetItems(tables)
}

func (m *model) updateSchemaView() {
	i, ok := m.tableList.SelectedItem().(item)
	if !ok {
		return
	}
	tableName := string(i)
	var query string
	if m.dbType == "mysql" {
		query = fmt.Sprintf("DESCRIBE %s.%s", m.activeDB, tableName)
	} else {
		query = fmt.Sprintf("PRAGMA table_info(%s)", tableName)
	}

	rows, err := m.db.Query(query)
	if err != nil {
		m.schemaView.SetContent(fmt.Sprintf("Error: %v", err))
		return
	}
	defer rows.Close()

	cols, _ := rows.Columns()
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Schema for %s:\n\n", tableName))
	for rows.Next() {
		values := make([]interface{}, len(cols))
		valuePtrs := make([]interface{}, len(cols))
		for j := range values {
			valuePtrs[j] = &values[j]
		}
		rows.Scan(valuePtrs...)
		for _, val := range values {
			if val == nil {
				sb.WriteString("NULL ")
			} else if b, ok := val.([]byte); ok {
				sb.WriteString(string(b) + " ")
			} else {
				sb.WriteString(fmt.Sprintf("%v ", val))
			}
		}
		sb.WriteString("\n")
	}
	m.schemaView.SetContent(sb.String())
}

func (m model) handleQuery() (model, tea.Cmd) {
	query := m.inputs[0].Value()
	rows, err := m.db.Query(query)
	if err != nil {
		m.err = err
		return m, nil
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		m.err = err
		return m, nil
	}

	var columns []table.Column
	for _, col := range cols {
		columns = append(columns, table.Column{Title: col, Width: 15})
	}

	var tableRows []table.Row
	var results []map[string]interface{}

	for rows.Next() {
		values := make([]interface{}, len(cols))
		valuePtrs := make([]interface{}, len(cols))
		for i := range values {
			valuePtrs[i] = &values[i]
		}

		if err := rows.Scan(valuePtrs...); err != nil {
			m.err = err
			return m, nil
		}

		row := make(table.Row, len(cols))
		resMap := make(map[string]interface{})
		for i, val := range values {
			var displayVal interface{}
			switch v := val.(type) {
			case nil:
				displayVal = "NULL"
			case []byte:
				displayVal = string(v)
			default:
				displayVal = v
			}
			row[i] = fmt.Sprintf("%v", displayVal)
			resMap[cols[i]] = displayVal
		}
		tableRows = append(tableRows, row)
		results = append(results, resMap)
	}

	t := table.New(
		table.WithColumns(columns),
		table.WithRows(tableRows),
		table.WithFocused(true),
		table.WithHeight(m.height-12),
	)

	s := table.DefaultStyles()
	s.Header = s.Header.BorderStyle(lipgloss.NormalBorder()).BorderBottom(true)
	s.Selected = s.Selected.Foreground(lipgloss.Color("229")).Background(lipgloss.Color("57"))
	t.SetStyles(s)

	m.resTable = t
	m.results = results
	
	jsonBytes, _ := json.MarshalIndent(results, "", "  ")
	m.jsonView.SetContent(string(jsonBytes))

	m.state = stateResult
	m.err = nil
	return m, nil
}

func (m model) View() string {
	var s string

	switch m.state {
	case stateSelectDB:
		s = titleStyle.Render("Select Database Type:") + "\n\n"
		mysql := "  MySQL"
		sqlite := "  SQLite"
		if m.dbType == "mysql" {
			mysql = "> MySQL"
		} else {
			sqlite = "> SQLite"
		}
		s += fmt.Sprintf("%s\n%s\n\n(up/down to select, enter to continue, ctrl+q to quit)\n", mysql, sqlite)

	case stateInputHost, stateInputPort, stateInputUser, stateInputPass, stateInputSQLitePath:
		s = titleStyle.Render(fmt.Sprintf("Connecting to %s", strings.ToUpper(m.dbType))) + "\n\n"
		if len(m.inputs) > 0 {
			s += m.inputs[0].View() + "\n\n"
		}
		s += "(enter to continue, esc to restart, ctrl+q to quit)\n"

	case stateExplorer:
		dbStyle := baseStyle
		tabStyle := baseStyle
		if m.focus == focusDB {
			dbStyle = focusStyle
		} else {
			tabStyle = focusStyle
		}

		panes := lipgloss.JoinHorizontal(lipgloss.Top,
			dbStyle.Width(m.width/4).Render(m.dbList.View()),
			tabStyle.Width(m.width/4).Render(m.tableList.View()),
			baseStyle.Width(m.width/2-2).Render(m.schemaView.View()),
		)
		s = titleStyle.Render(fmt.Sprintf("Explorer: %s", m.activeDB)) + "\n\n"
		s += panes + "\n\n"
		s += "(tab: switch panes, enter: select table, esc: disconnect, ctrl+q: quit)\n"

	case stateQuery:
		s = titleStyle.Render("Edit Query") + "\n\n"
		if len(m.inputs) > 0 {
			s += m.inputs[0].View() + "\n\n"
		}
		s += "(enter: execute, esc: explorer, ctrl+q: quit)\n"

	case stateResult:
		title := "Query Results (Table Mode)"
		content := baseStyle.Render(m.resTable.View())
		if m.viewMode == "json" {
			title = "Query Results (JSON Mode)"
			content = baseStyle.Render(m.jsonView.View())
		}
		s = titleStyle.Render(title) + "\n\n"
		s += content + "\n\n"
		s += "(tab: toggle Table/JSON, arrows: scroll, esc: back to query, ctrl+q: quit)\n"
	}

	if m.err != nil {
		s += errorStyle.Render(fmt.Sprintf("\nError: %v", m.err))
	}

	return s
}

func main() {
	p := tea.NewProgram(initialModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		log.Fatal(err)
		os.Exit(1)
	}
}
