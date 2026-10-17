// Package csvfile reads the CSV files companies upload to Grantra: HR
// exports and account lists. Both have a header row whose column names are
// matched loosely ("Employee ID" is employee_id) and can be renamed with a
// mapping, and both are read a row at a time, so a large file is never held
// twice in memory.
package csvfile

import (
	"bufio"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode"
)

// File reads an uploaded CSV file row by row.
type File struct {
	cr      *csv.Reader
	mapping map[string]string
	// col is the column of each field Grantra knows; extra holds every other
	// column, under its normalized name.
	col   map[string]int
	extra map[int]string
	row   []string
	line  int
}

// Open reads the header of r. fields are the names Grantra reads; mapping
// gives, for fields the file names differently, the column that holds them.
// Columns that are neither a field nor mapped are extra (see Extra).
func Open(r io.Reader, fields []string, mapping map[string]string) (*File, error) {
	br := bufio.NewReaderSize(r, 64<<10)
	// Spreadsheet programs like to start UTF-8 files with a byte order mark.
	if b, err := br.Peek(3); err == nil && string(b) == "\xef\xbb\xbf" {
		br.Discard(3) //nolint:errcheck // the bytes were just peeked
	}
	cr := csv.NewReader(br)
	cr.TrimLeadingSpace = true
	// Values are copied out of each row, so the row slice can be reused.
	cr.ReuseRecord = true

	header, err := cr.Read()
	if errors.Is(err, io.EOF) {
		return nil, errors.New("the file is empty")
	}
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	byName := map[string]int{}
	for i, h := range header {
		name := Normalize(h)
		if name == "" {
			continue
		}
		if _, dup := byName[name]; dup {
			return nil, fmt.Errorf("column %q appears twice", strings.TrimSpace(h))
		}
		byName[name] = i
	}
	f := &File{cr: cr, mapping: mapping, col: map[string]int{}, extra: map[int]string{}}
	for _, field := range fields {
		name := field
		if c, ok := mapping[field]; ok {
			name = Normalize(c)
		}
		if i, ok := byName[name]; ok {
			f.col[field] = i
		}
	}
	taken := map[int]bool{}
	for _, i := range f.col {
		taken[i] = true
	}
	for name, i := range byName {
		// A column named like a field that was mapped elsewhere is ignored
		// rather than allowed to shadow the mapped one.
		if !taken[i] && !slices.Contains(fields, name) {
			f.extra[i] = name
		}
	}
	return f, nil
}

// Has reports whether the file has a column for field.
func (f *File) Has(field string) bool {
	_, ok := f.col[field]
	return ok
}

// Column is the name of the column that holds field, as the people who
// wrote the mapping know it, for messages.
func (f *File) Column(field string) string {
	return ColumnName(f.mapping, field)
}

// ColumnName is the column that holds field under mapping.
func ColumnName(mapping map[string]string, field string) string {
	if c, ok := mapping[field]; ok {
		return c
	}
	return field
}

// Next moves to the next row that is not blank and returns its line number
// in the file. It returns io.EOF after the last row. An error in the CSV
// itself (an unclosed quote, a row with too many columns) ends the read:
// the rest of the file cannot be trusted.
func (f *File) Next() (int, error) {
	for {
		row, err := f.cr.Read()
		if errors.Is(err, io.EOF) {
			return 0, io.EOF
		}
		if err != nil {
			var pe *csv.ParseError
			if errors.As(err, &pe) {
				return 0, fmt.Errorf("line %d: %v", pe.Line, pe.Err)
			}
			return 0, err
		}
		if blank(row) {
			continue
		}
		f.row = row
		f.line, _ = f.cr.FieldPos(0)
		return f.line, nil
	}
}

// Get returns the trimmed value of field in the current row, "" when the
// file has no column for it.
func (f *File) Get(field string) string {
	if i, ok := f.col[field]; ok && i < len(f.row) {
		return strings.TrimSpace(f.row[i])
	}
	return ""
}

// Extra calls fn with the name and value of every extra column that is not
// empty in the current row.
func (f *File) Extra(fn func(name, value string)) {
	for i, name := range f.extra {
		if i < len(f.row) {
			if v := strings.TrimSpace(f.row[i]); v != "" {
				fn(name, v)
			}
		}
	}
}

// ValidateMapping checks that a mapping only renames known fields, to
// columns with a name.
func ValidateMapping(mapping map[string]string, fields []string) error {
	for field, col := range mapping {
		if !slices.Contains(fields, field) {
			return fmt.Errorf("columns: unknown field %q (known: %s)", field, strings.Join(fields, ", "))
		}
		if Normalize(col) == "" {
			return fmt.Errorf("columns: empty column name for %q", field)
		}
	}
	return nil
}

// Normalize turns a column header into snake_case: "Employee ID" becomes
// "employee_id".
func Normalize(h string) string {
	var b strings.Builder
	under := false
	for _, r := range strings.ToLower(strings.TrimSpace(h)) {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			if under && b.Len() > 0 {
				b.WriteByte('_')
			}
			under = false
			b.WriteRune(r)
		default:
			under = true
		}
	}
	return b.String()
}

func blank(row []string) bool {
	for _, v := range row {
		if strings.TrimSpace(v) != "" {
			return false
		}
	}
	return true
}
