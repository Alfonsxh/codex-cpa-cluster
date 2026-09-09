package usagereport

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"strings"
	"testing"
	"time"

	"github.com/Alfonsxh/codex-cpa-pool/internal/usage"
	"github.com/xuri/excelize/v2"
)

func TestCurrentBindingsDoNotReattributeHistoricalUsage(t *testing.T) {
	r := reportFixture(t)
	if r.Users[0].CurrentAccount != "beta" || r.Users[0].Current.Accounts != 2 {
		t.Fatalf("current binding lost or replaced actual accounts: %+v", r.Users[0])
	}
	for _, e := range r.Accounts {
		switch e.ID {
		case "alpha":
			if e.BoundUsers != 1 || e.Current.Users != 2 || e.Current.TotalTokens != 130 {
				t.Fatalf("historical usage was reassigned: %+v", e)
			}
		case "beta":
			if e.BoundUsers != 1 || e.Current.TotalTokens != 25 {
				t.Fatalf("binding must not move historical usage: %+v", e)
			}
		case "idle":
			if e.BoundUsers != 1 || e.Current.RequestCount != 0 {
				t.Fatalf("idle binding missing: %+v", e)
			}
		}
	}
}

func TestReportRanksRawUsageWithStableTies(t *testing.T) {
	period := reportFixture(t).Period
	r, err := Build(period, Catalog{}, []usage.ReportUsageRow{
		{Window: 7, Account: "b", User: "b", Usage: usage.WeightedMetrics{RawMetrics: usage.RawMetrics{RequestCount: 1, TotalTokens: 200}, WeightedTokens: 200}},
		{Window: 7, Account: "c", User: "c", Usage: usage.WeightedMetrics{RawMetrics: usage.RawMetrics{RequestCount: 1, TotalTokens: 100}, WeightedTokens: 400}},
		{Window: 7, Account: "a", User: "a", Usage: usage.WeightedMetrics{RawMetrics: usage.RawMetrics{RequestCount: 1, TotalTokens: 200}, WeightedTokens: 800}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, entries := range [][]Entry{r.Accounts, r.Users} {
		if entries[0].ID != "a" || entries[1].ID != "b" || entries[2].ID != "c" {
			t.Fatalf("raw ranking and ID tie break: %+v", entries)
		}
	}
}

func TestDetailValuesDatesAlignmentAndFilterBounds(t *testing.T) {
	zone, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	period, err := ResolvePeriod("2026-08-31", time.Date(2026, 9, 9, 0, 0, 0, 0, zone), zone)
	if err != nil {
		t.Fatal(err)
	}
	last := time.Date(2026, 9, 1, 16, 20, 30, 0, time.UTC).Unix()
	r, err := Build(period, Catalog{Accounts: map[string]string{"alpha": "alpha@example.com"}, Teams: map[string]string{"team": "研发"}, UserTeams: map[string]string{"alice@example.com": "team"}, UserAccounts: map[string]string{"alice@example.com": "alpha"}}, []usage.ReportUsageRow{
		{Window: 9, Account: "alpha", User: "alice@example.com", Usage: usage.WeightedMetrics{RawMetrics: usage.RawMetrics{RequestCount: 1, SuccessCount: 1, InputTokens: 400, CachedTokens: 300, OutputTokens: 100, TotalTokens: 510, LastUsedAt: last}, WeightedTokens: 2040}},
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := XLSX(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, tc := range []struct{ sheet, address, want string }{
		{accountSheet, "E8", "1"}, {accountSheet, "F8", "1"},
		{accountSheet, "H8", "400"}, {accountSheet, "I8", "300"}, {accountSheet, "J8", "100"}, {accountSheet, "K8", "510"}, {accountSheet, "L8", "2,040"},
		{accountSheet, "Q8", "2026-09-02 00:20"},
		{userSheet, "D8", "alpha"}, {userSheet, "G8", "400"}, {userSheet, "H8", "300"}, {userSheet, "I8", "100"}, {userSheet, "J8", "510"},
		{userSheet, "P8", "2026-09-02 00:20"}, {dailySheet, "B10", "2026-09-02"},
		{summarySheet, "B8", "510"}, {summarySheet, "E8", "2,040"},
	} {
		got, err := f.GetCellValue(tc.sheet, tc.address)
		if err != nil || got != tc.want {
			t.Fatalf("%s!%s=%q want=%q err=%v", tc.sheet, tc.address, got, tc.want, err)
		}
	}
	for _, tc := range []struct{ sheet, address string }{{accountSheet, "Q8"}, {userSheet, "P8"}, {dailySheet, "B10"}} {
		kind, err := f.GetCellType(tc.sheet, tc.address)
		if err != nil || (kind != excelize.CellTypeNumber && kind != excelize.CellTypeUnset) {
			t.Fatalf("date is not sortable numeric data: %s!%s %v %v", tc.sheet, tc.address, kind, err)
		}
	}
	for _, tc := range []struct {
		sheet             string
		lastCol, totalRow int
	}{{teamSheet, 11, 9}, {accountSheet, 17, 9}, {userSheet, 16, 9}, {dailySheet, 14, 15}} {
		for col := 2; col <= tc.lastCol; col++ {
			var styles []*excelize.Style
			for _, row := range []int{7, 8, tc.totalRow} {
				id, err := f.GetCellStyle(tc.sheet, cell(col, row))
				if err != nil {
					t.Fatal(err)
				}
				s, err := f.GetStyle(id)
				if err != nil {
					t.Fatal(err)
				}
				styles = append(styles, s)
			}
			for _, s := range styles[1:] {
				if s.Alignment.Horizontal != styles[0].Alignment.Horizontal || s.Alignment.Indent != styles[0].Alignment.Indent {
					t.Fatalf("header/body/total alignment diverged in %s column %d", tc.sheet, col)
				}
			}
			if styles[2].Fill.Color[0] != totalColor || !styles[2].Font.Bold {
				t.Fatalf("total styling lost in %s column %d", tc.sheet, col)
			}
		}
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	wantFilters := map[string]string{"xl/worksheets/sheet2.xml": "B7:K8", "xl/worksheets/sheet3.xml": "B7:Q8", "xl/worksheets/sheet4.xml": "B7:P8", "xl/worksheets/sheet5.xml": "B7:N14"}
	for _, part := range archive.File {
		want, ok := wantFilters[part.Name]
		if !ok {
			continue
		}
		reader, err := part.Open()
		if err != nil {
			t.Fatal(err)
		}
		var sheet struct {
			Filter struct {
				Ref string `xml:"ref,attr"`
			} `xml:"autoFilter"`
		}
		err = xml.NewDecoder(reader).Decode(&sheet)
		reader.Close()
		if err != nil || strings.ReplaceAll(sheet.Filter.Ref, "$", "") != want {
			t.Fatalf("filter includes totals or misses columns: %s %+v %v", part.Name, sheet, err)
		}
		delete(wantFilters, part.Name)
	}
	if len(wantFilters) != 0 {
		t.Fatalf("missing sheet filters: %v", wantFilters)
	}
}
