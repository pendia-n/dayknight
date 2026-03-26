package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textinput"
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
)

type sessionState int

const (
	stateSelectDB sessionState = iota
	stateInputHost
	stateInputPort
	stateInputUser
	stateInputPass
	stateInputDBName
	stateInputSQLitePath
	stateQuery
	stateResult
)

type model struct {
	state      sessionState
	db         *sql.DB
	dbType     string // "mysql" or "sqlite3"
	inputs     []textinput.Model
	focusIndex int
	table      table.Model
	err        error
	width      int
	height     int

	// MySQL details
	host     string
	port     string
	user     string
	password string
	dbname   string

	// SQLite details
	sqlitePath string
}

func initialModel() model {
	m := model{
		state:  stateSelectDB,
		dbType: "mysql", // default selection
	}
	return m
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
			if m.state == stateResult {
				m.state = stateQuery
				m.inputs[0].Focus()
				return m, nil
			}
			if m.state == stateQuery {
				m.state = stateSelectDB
				if m.db != nil {
					m.db.Close()
				}
				return m, nil
			}
			m.state = stateSelectDB
			return m, nil

		case "up", "down":
			if m.state == stateSelectDB {
				if m.dbType == "mysql" {
					m.dbType = "sqlite3"
				} else {
					m.dbType = "mysql"
				}
			}

		case "enter":
			return m.handleEnter()
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	}

	if m.state >= stateInputHost && m.state <= stateInputSQLitePath || m.state == stateQuery {
		if len(m.inputs) > 0 {
			m.inputs[0], cmd = m.inputs[0].Update(msg)
		}
	} else if m.state == stateResult {
		m.table, cmd = m.table.Update(msg)
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
		m.state = stateInputDBName
		m.setupInput("Database Name", "", false)
	case stateInputDBName:
		m.dbname = m.inputs[0].Value()
		return m.connect()
	case stateInputSQLitePath:
		m.sqlitePath = m.inputs[0].Value()
		return m.connect()
	case stateQuery:
		return m.handleQuery()
	}
	return m, nil
}

func (m model) connect() (model, tea.Cmd) {
	var driver, dsn string
	if m.dbType == "mysql" {
		driver = "mysql"
		dsn = fmt.Sprintf("%s:%s@tcp(%s:%s)/%s", m.user, m.password, m.host, m.port, m.dbname)
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
	m.state = stateQuery
	m.err = nil
	m.setupInput("SELECT * FROM users LIMIT 10", "", false)
	return m, nil
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
		for i, val := range values {
			switch v := val.(type) {
			case nil:
				row[i] = "NULL"
			case []byte:
				row[i] = string(v)
			default:
				row[i] = fmt.Sprintf("%v", v)
			}
		}
		tableRows = append(tableRows, row)
	}

	t := table.New(
		table.WithColumns(columns),
		table.WithRows(tableRows),
		table.WithFocused(true),
		table.WithHeight(15),
	)

	s := table.DefaultStyles()
	s.Header = s.Header.
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color("240")).
		BorderBottom(true).
		Bold(false)
	s.Selected = s.Selected.
		Foreground(lipgloss.Color("229")).
		Background(lipgloss.Color("57")).
		Bold(false)
	t.SetStyles(s)

	m.table = t
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

	case stateInputHost, stateInputPort, stateInputUser, stateInputPass, stateInputDBName, stateInputSQLitePath:
		s = titleStyle.Render(fmt.Sprintf("Connecting to %s", strings.ToUpper(m.dbType))) + "\n\n"
		if len(m.inputs) > 0 {
			s += m.inputs[0].View() + "\n\n"
		}
		s += "(enter to continue, esc to restart, ctrl+q to quit)\n"

	case stateQuery:
		s = titleStyle.Render(fmt.Sprintf("Connected to %s", m.dbType)) + "\n\n"
		s += "Enter SQL Query:\n\n"
		if len(m.inputs) > 0 {
			s += m.inputs[0].View() + "\n\n"
		}
		s += "(enter to execute, esc to disconnect, ctrl+q to quit)\n"

	case stateResult:
		s = baseStyle.Render(m.table.View()) + "\n\n(esc to return to query, ctrl+q to quit)\n"
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
