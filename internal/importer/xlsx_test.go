package importer

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"

	"chwr/internal/auth"
	"chwr/internal/domain"
)

// workbook writes rows to a real .xlsx in memory, so the reader is exercised
// against the format rather than against a stub of it.
func workbook(t *testing.T, sheet string, rows [][]string) *bytes.Reader {
	t.Helper()
	wb := excelize.NewFile()
	defer wb.Close()

	if sheet != "Sheet1" {
		if _, err := wb.NewSheet(sheet); err != nil {
			t.Fatalf("new sheet: %v", err)
		}
		wb.DeleteSheet("Sheet1")
	}
	for i, row := range rows {
		cell, err := excelize.CoordinatesToCellName(1, i+1)
		if err != nil {
			t.Fatalf("cell name: %v", err)
		}
		values := make([]any, len(row))
		for j, v := range row {
			values[j] = v
		}
		if err := wb.SetSheetRow(sheet, cell, &values); err != nil {
			t.Fatalf("write row: %v", err)
		}
	}

	var buf bytes.Buffer
	if err := wb.Write(&buf); err != nil {
		t.Fatalf("write workbook: %v", err)
	}
	return bytes.NewReader(buf.Bytes())
}

var headerCells = strings.Split(strings.TrimSuffix(header, "\n"), ",")

// The two readers are two ways into the same validation. A district that keeps
// its register in Excel and one that keeps it in a CSV must get the same
// answer, or the format becomes part of the result.
func TestXLSXAndCSVAgree(t *testing.T) {
	body := [][]string{
		{"Grace", "Okello", "female", "vht", "34", "CM90210987654X", "ABIM", "MORULEM", "ALEREK", "KANU-EAST", ""},
		{"Moses", "Ojok", "m", "chew", "", "", "ABIM", "MORULEM", "ALEREK", "", ""},
		{"Sarah", "Akello", "f", "vht", "", "", "ABIM", "MORULEM", "ALEREK", "BUHOBA A", ""},
		{"Betty", "Aber", "f", "nurse", "7", "NOPE", "ABIM", "MORULEM", "ALEREK", "KANU-EAST", ""},
	}

	var csvBody strings.Builder
	for _, row := range body {
		csvBody.WriteString(strings.Join(row, ",") + "\n")
	}
	fromCSV := validate(t, auth.National(), csvBody.String(), nil)

	xlsx, err := ReadXLSX("register.xlsx", workbook(t, "Sheet1", append([][]string{headerCells}, body...)))
	if err != nil {
		t.Fatalf("read xlsx: %v", err)
	}
	fromXLSX, err := New(&fakeLookup{}, auth.National()).Validate(context.Background(), xlsx)
	if err != nil {
		t.Fatalf("validate xlsx: %v", err)
	}

	if len(fromCSV) != len(fromXLSX) {
		t.Fatalf("csv staged %d rows, xlsx staged %d", len(fromCSV), len(fromXLSX))
	}
	for i := range fromCSV {
		c, x := fromCSV[i], fromXLSX[i]
		if c.Row.Status != x.Row.Status || c.Row.Number != x.Row.Number {
			t.Errorf("row %d: csv %s at line %d, xlsx %s at line %d",
				i, c.Row.Status, c.Row.Number, x.Row.Status, x.Row.Number)
		}
		if !sameRecord(c.Record, x.Record) {
			t.Errorf("row %d parsed differently:\n csv  %+v\n xlsx %+v", i, c.Record, x.Record)
		}
		if fmt.Sprint(codes(c)) != fmt.Sprint(codes(x)) {
			t.Errorf("row %d: csv %v, xlsx %v", i, codes(c), codes(x))
		}
	}
}

// Only the first worksheet is read. A workbook with the register on sheet three
// is a question the operator can answer by moving it; guessing which sheet was
// meant is the kind of guess that reads as a right answer.
func TestXLSXReadsOnlyTheFirstSheet(t *testing.T) {
	wb := excelize.NewFile()
	defer wb.Close()
	if _, err := wb.NewSheet("Register"); err != nil {
		t.Fatalf("new sheet: %v", err)
	}
	values := make([]any, len(headerCells))
	for i, h := range headerCells {
		values[i] = h
	}
	// Sheet1 stays first and holds notes; the register is on the second sheet.
	if err := wb.SetSheetRow("Sheet1", "A1", &[]any{"Notes for the district"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := wb.SetSheetRow("Register", "A1", &values); err != nil {
		t.Fatalf("write: %v", err)
	}

	var buf bytes.Buffer
	if err := wb.Write(&buf); err != nil {
		t.Fatalf("write workbook: %v", err)
	}

	_, err := ReadXLSX("book.xlsx", bytes.NewReader(buf.Bytes()))
	var missing *MissingColumnsError
	if !asMissing(err, &missing) {
		t.Fatalf("err = %v, want the first sheet's columns to be refused", err)
	}
	if len(missing.Columns) == 0 {
		t.Error("no columns named")
	}
}

func TestXLSXEmptyWorkbook(t *testing.T) {
	if _, err := ReadXLSX("empty.xlsx", workbook(t, "Sheet1", nil)); err != ErrNoHeader {
		t.Errorf("err = %v, want ErrNoHeader", err)
	}
}

// A workbook's rows can be ragged: excelize returns only as many cells as the
// sheet holds, so a row whose trailing columns were never typed is short.
func TestXLSXShortRowsReadAsEmptyCells(t *testing.T) {
	rows := [][]string{
		headerCells,
		{"Grace", "Okello", "female", "chew", "", "", "ABIM", "MORULEM", "ALEREK"},
	}
	f, err := ReadXLSX("short.xlsx", workbook(t, "Sheet1", rows))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	staged, err := New(&fakeLookup{}, auth.National()).Validate(context.Background(), f)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if s := only(t, staged); s.Row.Status != domain.RowReady {
		t.Fatalf("status = %s (%v)", s.Row.Status, s.Row.Problems)
	}
}

// sameRecord compares by value. The record is full of pointers, because "0
// households" and "not asked" are different answers throughout the register, so
// two equal values are two different pointers. Comparing the JSON the row would
// be staged with is the same comparison the commit cares about.
func sameRecord(a, b Record) bool {
	left, err := a.Encode()
	if err != nil {
		return false
	}
	right, err := b.Encode()
	if err != nil {
		return false
	}
	return string(left) == string(right)
}
