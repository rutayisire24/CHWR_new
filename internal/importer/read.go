package importer

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/xuri/excelize/v2"

	"chwr/internal/domain"
)

// Limits on one upload. A file past these is a migration rather than an
// import, and belongs on the seeder path in docs/seeding.md where it can be
// checked against its source before it runs.
const (
	MaxRows = 10000
	// MaxFileBytes is the file's own limit. auth.MaxMultipartBytes is a little
	// larger so that an oversized file is refused here, by name, rather than by
	// the middleware with a bare 413.
	MaxFileBytes = 10 << 20
)

// ErrTooManyRows and friends are the file-level refusals: they stop the upload
// before a single row is staged, because there is nothing to review when the
// file itself is wrong.
var (
	ErrTooManyRows = fmt.Errorf("a file may hold at most %d rows", MaxRows)
	ErrNoRows      = errors.New("the file has a header but no rows")
	ErrNoHeader    = errors.New("the file is empty")
	ErrNoSheet     = errors.New("the workbook has no sheets")
)

// MissingColumnsError names the required columns a file does not carry. It is
// its own type because the upload page lists them, rather than printing one
// sentence.
type MissingColumnsError struct{ Columns []string }

func (e *MissingColumnsError) Error() string {
	return "the file is missing " + strings.Join(e.Columns, ", ")
}

// File is an upload read into memory and no further: the header, and one Row
// per line, with every value still exactly as the file wrote it.
type File struct {
	Name   string
	Format string
	Header Header
	Rows   []Row
}

// Row is one line of the file. Number is the line as a spreadsheet counts it —
// the header is line 1 — so a message points at what the operator sees.
type Row struct {
	Number int
	cells  []string
	header Header
}

// Value reads a canonical column, trimmed. A column the file does not carry,
// and a row too short to reach it, both read as empty: a ragged line is a
// missing trailing comma far more often than it is a misaligned row.
func (r Row) Value(column string) string {
	i, ok := r.header.index[column]
	if !ok || i >= len(r.cells) {
		return ""
	}
	return strings.TrimSpace(r.cells[i])
}

// Raw is the line as it arrived: keys as the file spelled them, values
// untouched. It is what reaches import_rows.raw, because a refusal has to be
// explainable a month later and "what did the file actually say" is the first
// question.
func (r Row) Raw() map[string]string {
	out := make(map[string]string, len(r.header.Spelled))
	for i, name := range r.header.Spelled {
		if i < len(r.cells) {
			out[name] = r.cells[i]
		}
	}
	return out
}

// Blank reports whether the line carries no identity at all — no name, no NIN,
// no cadre. Such a line is skipped rather than refused: it is the template's
// own example row, or the trailing rows a spreadsheet keeps after someone
// deletes the contents of a cell but not the row.
func (r Row) Blank() bool {
	for _, column := range []string{ColFirstName, ColLastName, ColNIN, ColCadre} {
		if r.Value(column) != "" {
			return false
		}
	}
	return true
}

// Read dispatches on the format the handler determined from the filename.
func Read(name, format string, in io.Reader) (*File, error) {
	switch format {
	case domain.FormatCSV:
		return ReadCSV(name, in)
	case domain.FormatXLSX:
		return ReadXLSX(name, in)
	}
	return nil, fmt.Errorf("unknown import format %q", format)
}

// ReadCSV reads a comma-separated file.
//
// LazyQuotes is on: a stray quote inside an unquoted field is a thing Excel
// exports and a district cannot see, and refusing the whole file over one would
// be a message nobody can act on. FieldsPerRecord is off for the same reason —
// a short line reads as empty cells rather than as a broken file.
func ReadCSV(name string, in io.Reader) (*File, error) {
	r := csv.NewReader(skipBOM(in))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	r.TrimLeadingSpace = true

	header, err := r.Read()
	if err == io.EOF {
		return nil, ErrNoHeader
	}
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}

	f := &File{Name: name, Format: domain.FormatCSV, Header: ReadHeader(header)}
	if missing := f.Header.Missing(); len(missing) > 0 {
		return nil, &MissingColumnsError{Columns: missing}
	}

	line := 1
	for {
		cells, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read line %d: %w", line+1, err)
		}
		line++
		if len(f.Rows) >= MaxRows {
			return nil, ErrTooManyRows
		}
		f.Rows = append(f.Rows, Row{Number: line, cells: cells, header: f.Header})
	}
	return f, f.check()
}

// ReadXLSX reads the first worksheet of a workbook.
//
// Only the first sheet: a workbook with the register on sheet three is a
// question the operator can answer by moving it, and guessing which sheet was
// meant is the kind of guess that reads as a right answer.
//
// Values arrive as excelize formats them, which is what the cell displays. A
// number formatted as scientific notation therefore imports as it looks — one
// more reason the location_code column exists, and why the codes belong in a
// text column in the source workbook.
func ReadXLSX(name string, in io.Reader) (*File, error) {
	wb, err := excelize.OpenReader(in)
	if err != nil {
		return nil, fmt.Errorf("read workbook: %w", err)
	}
	defer wb.Close()

	sheets := wb.GetSheetList()
	if len(sheets) == 0 {
		return nil, ErrNoSheet
	}

	rows, err := wb.Rows(sheets[0])
	if err != nil {
		return nil, fmt.Errorf("read sheet %q: %w", sheets[0], err)
	}
	defer rows.Close()

	var f *File
	line := 0
	for rows.Next() {
		cells, err := rows.Columns()
		if err != nil {
			return nil, fmt.Errorf("read line %d: %w", line+1, err)
		}
		line++

		if f == nil {
			f = &File{Name: name, Format: domain.FormatXLSX, Header: ReadHeader(cells)}
			if missing := f.Header.Missing(); len(missing) > 0 {
				return nil, &MissingColumnsError{Columns: missing}
			}
			continue
		}
		if len(f.Rows) >= MaxRows {
			return nil, ErrTooManyRows
		}
		f.Rows = append(f.Rows, Row{Number: line, cells: cells, header: f.Header})
	}
	if f == nil {
		return nil, ErrNoHeader
	}
	return f, f.check()
}

// check refuses a file that carries no importable line. A file of nothing but
// the template's example row lands here, which is the right answer: there is
// nothing to review, and a report of zero rows reads as a system failure rather
// than as an empty file.
func (f *File) check() error {
	for _, r := range f.Rows {
		if !r.Blank() {
			return nil
		}
	}
	return ErrNoRows
}

// skipBOM drops the byte-order mark Excel writes in front of a UTF-8 CSV. Left
// in place it becomes part of the first header cell, and the file is refused
// for missing a column it plainly has.
func skipBOM(in io.Reader) io.Reader {
	buf := make([]byte, 3)
	n, err := io.ReadFull(in, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return in
	}
	if n == 3 && buf[0] == 0xEF && buf[1] == 0xBB && buf[2] == 0xBF {
		return in
	}
	return io.MultiReader(strings.NewReader(string(buf[:n])), in)
}

// FormatFor names the reader a filename asks for, and reports whether it is one
// we have.
func FormatFor(filename string) (string, bool) {
	switch strings.ToLower(filename[strings.LastIndex(filename, ".")+1:]) {
	case "csv":
		return domain.FormatCSV, true
	case "xlsx", "xlsm":
		return domain.FormatXLSX, true
	}
	return "", false
}
