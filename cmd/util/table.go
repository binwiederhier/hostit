// Package clitable renders the CLI's tables: one look for every list the
// hostit binaries print. It wraps lipgloss, whose color handling degrades to
// plain text when stdout is not a terminal, so piped output stays grep-able.
package util

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
)

var (
	headerStyle = lipgloss.NewStyle().Bold(true).Padding(0, 1)
	cellStyle   = lipgloss.NewStyle().Padding(0, 1)
	borderStyle = lipgloss.NewStyle().Faint(true)
	titleStyle  = lipgloss.NewStyle().Bold(true)
)

// Render draws one table with a header row, sized to its content.
func Render(headers []string, rows [][]string) string {
	t := table.New().
		Border(lipgloss.NormalBorder()).
		BorderStyle(borderStyle).
		StyleFunc(func(row, _ int) lipgloss.Style {
			if row == table.HeaderRow {
				return headerStyle
			}
			return cellStyle
		}).
		Headers(headers...).
		Rows(rows...)
	return t.String()
}

// Title styles a section heading above a table or summary block.
func Title(text string) string {
	return titleStyle.Render(text)
}
