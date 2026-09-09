package usagereport

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"
)

const ContentType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"

const (
	summarySheet   = "用量总览"
	teamSheet      = "团队统计"
	accountSheet   = "账号明细"
	userSheet      = "用户使用明细"
	dailySheet     = "每日趋势"
	tableHeaderRow = 7
	tableDataRow   = 8
	inkColor       = "18283F"
	mutedColor     = "718096"
	blueColor      = "3168CA"
	lineColor      = "DCE4EF"
	stripeColor    = "F7F9FC"
	totalColor     = "EAF0F9"
	amberColor     = "B87521"
	tealColor      = "278577"
)

type XLSXOptions struct{ WithUnits bool }

type column struct {
	label    string
	width    float64
	kind     string
	align    string
	emphasis bool
}

type cellStyleKey struct {
	kind, fill, align string
	bold              bool
}
type tokenStyleKey struct {
	base   int
	format string
}

type workbook struct {
	file        *excelize.File
	ctx         context.Context
	err         error
	withUnits   bool
	styles      map[cellStyleKey]int
	tokenStyles map[tokenStyleKey]int
}

// XLSX materializes a complete snapshot before HTTP headers are written. Labels
// remain strings; report data never becomes executable formulas or hyperlinks.
func XLSX(ctx context.Context, r Report, options ...XLSXOptions) ([]byte, error) {
	x := &workbook{file: excelize.NewFile(), ctx: ctx, styles: map[cellStyleKey]int{}, tokenStyles: map[tokenStyleKey]int{}}
	if len(options) > 0 {
		x.withUnits = options[0].WithUnits
	}
	defer x.file.Close()
	x.check(x.file.SetSheetName("Sheet1", summarySheet))
	for _, name := range []string{teamSheet, accountSheet, userSheet, dailySheet} {
		_, err := x.file.NewSheet(name)
		x.check(err)
	}
	x.details(r)
	x.daily(r)
	x.summary(r)
	x.file.SetActiveSheet(0)
	if x.err != nil {
		return nil, x.err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	buffer, err := x.file.WriteToBuffer()
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func (x *workbook) check(err error) {
	if x.err == nil {
		x.err = err
	}
	if x.err == nil {
		x.err = x.ctx.Err()
	}
}

func (x *workbook) style(style *excelize.Style) int {
	id, err := x.file.NewStyle(style)
	x.check(err)
	return id
}

func cell(col, row int) string { name, _ := excelize.CoordinatesToCellName(col, row); return name }
func ptr[T any](value T) *T    { return &value }

func (x *workbook) cellStyle(kind, fill, align string, bold bool) int {
	key := cellStyleKey{kind, fill, align, bold}
	if id, ok := x.styles[key]; ok {
		return id
	}
	s := &excelize.Style{
		Font:      &excelize.Font{Family: "Arial", Size: 11, Color: inkColor, Bold: bold},
		Fill:      excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{fill}},
		Alignment: &excelize.Alignment{Vertical: "center", Horizontal: align, Indent: 1},
	}
	switch kind {
	case "text":
		s.Alignment.WrapText = true
	case "header":
		s.Font.Color = "FFFFFF"
		s.Font.Bold = true
	case "title":
		s.Font.Size = 17
		s.Font.Bold = true
	case "section":
		s.Font.Size = 14
		s.Font.Bold = true
	case "note":
		s.Font.Size = 10
		s.Font.Color = mutedColor
	case "denominator":
		s.Font.Size = 10
		s.Font.Color = mutedColor
		s.CustomNumFmt = ptr(`"/ "#,##0`)
	case "kpi", "rawKPI":
		s.Font.Size = 20
		s.Font.Bold = true
		s.NumFmt = 3
		if kind == "rawKPI" {
			s.Font.Color = blueColor
		}
	case "number", "token":
		s.NumFmt = 3
	case "percent":
		s.CustomNumFmt = ptr("0.0%")
	case "delta":
		s.CustomNumFmt = ptr("+0.0%;-0.0%;0.0%")
	case "date":
		s.CustomNumFmt = ptr("yyyy-mm-dd hh:mm")
	case "day":
		s.CustomNumFmt = ptr("yyyy-mm-dd")
	case "factor":
		s.CustomNumFmt = ptr(`0.00"×"`)
	}
	if bold && kind == "token" {
		s.Font.Color = blueColor
	}
	x.styles[key] = x.style(s)
	return x.styles[key]
}

func (x *workbook) put(sheet, address string, value any, style int) {
	if x.err != nil {
		return
	}
	x.check(x.file.SetCellValue(sheet, address, value))
	x.check(x.file.SetCellStyle(sheet, address, address, style))
}

func (x *workbook) merged(sheet, from, to string, value any, style int) {
	if x.err != nil {
		return
	}
	x.check(x.file.MergeCell(sheet, from, to))
	x.put(sheet, from, value, style)
	x.check(x.file.SetCellStyle(sheet, from, to, style))
}

func compactTokenNumberFormat(amount float64) string {
	switch absolute := math.Abs(amount); {
	case absolute >= 1_000_000_000:
		return `0.00,,," B"`
	case absolute >= 1_000_000:
		return `0.00,," M"`
	case absolute >= 1_000:
		return `0.00," K"`
	default:
		return `#,##0" Token"`
	}
}

func (x *workbook) tokenStyle(base int, value any) int {
	if !x.withUnits {
		return base
	}
	var amount float64
	switch n := value.(type) {
	case int64:
		amount = float64(n)
	case float64:
		amount = n
	default:
		return base
	}
	key := tokenStyleKey{base, compactTokenNumberFormat(amount)}
	if id, ok := x.tokenStyles[key]; ok {
		return id
	}
	s, err := x.file.GetStyle(base)
	x.check(err)
	if x.err != nil {
		return base
	}
	s.NumFmt = 0
	s.CustomNumFmt = &key.format
	x.tokenStyles[key] = x.style(s)
	return x.tokenStyles[key]
}

func (x *workbook) setup(sheet string, r Report, lastCol int, note string) {
	last, _ := excelize.ColumnNumberToName(lastCol)
	x.check(x.file.SetColWidth(sheet, "A", "A", 4))
	x.check(x.file.SetColWidth(sheet, cellColumn(lastCol+1), cellColumn(lastCol+1), 4))
	x.check(x.file.SetRowHeight(sheet, 1, 18))
	x.check(x.file.SetRowHeight(sheet, 2, 30))
	title := sheet
	if sheet == summarySheet {
		title = "Token 用量周报"
	}
	x.merged(sheet, "B2", cell(lastCol-2, 2), title, x.cellStyle("title", "FFFFFF", "left", true))
	x.merged(sheet, cell(lastCol-1, 2), cell(lastCol, 2), "CCPA", x.cellStyle("note", "FFFFFF", "right", false))
	meta := fmt.Sprintf("%s — %s（结束不含）  %s", r.Period.Start.Format(time.DateTime), r.Period.End.Format(time.DateTime), r.Period.Start.Location())
	if r.Period.Partial() {
		meta += "  本周未结束"
	}
	x.merged(sheet, "B3", cell(lastCol, 3), meta, x.cellStyle("note", "FFFFFF", "left", false))
	x.check(x.file.SetRowHeight(sheet, 3, 24))
	x.check(x.file.SetRowHeight(sheet, 4, 12))
	x.check(x.file.SetCellStyle(sheet, "B4", cell(lastCol, 4), x.style(&excelize.Style{Border: []excelize.Border{{Type: "bottom", Color: lineColor, Style: 1}}})))
	x.merged(sheet, "B5", cell(lastCol, 5), note, x.cellStyle("note", "FFFFFF", "left", false))
	x.check(x.file.SetRowHeight(sheet, 5, 25))
	x.check(x.file.SetRowHeight(sheet, 6, 10))
	x.check(x.file.SetSheetView(sheet, 0, &excelize.ViewOptions{ShowGridLines: ptr(false), ZoomScale: ptr(90.0)}))
	x.check(x.file.SetPageLayout(sheet, &excelize.PageLayoutOptions{Orientation: ptr("landscape"), Size: ptr(9), FitToWidth: ptr(1), FitToHeight: ptr(0)}))
	x.check(x.file.SetColWidth(sheet, "B", last, 18))
}

func cellColumn(col int) string { name, _ := excelize.ColumnNumberToName(col); return name }

func ratio(n, d int64) any {
	if d <= 0 {
		return "—"
	}
	return float64(n) / float64(d)
}
func change(current, previous int64) any {
	if previous <= 0 {
		return "—"
	}
	return float64(current)/float64(previous) - 1
}
func comparisonStatus(previous Metrics) string {
	if previous.RequestCount == 0 {
		return "无请求记录"
	}
	return "有记录"
}
func activity(m Metrics) string {
	if m.RequestCount == 0 {
		return "无请求"
	}
	return "活跃"
}

// Excel dates have no timezone. Encode the configured business wall clock, not UTC.
func localExcelTime(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, time.UTC)
}
func lastUsed(m Metrics, zone *time.Location) any {
	if m.LastUsedAt <= 0 {
		return nil
	}
	return localExcelTime(time.Unix(m.LastUsedAt, 0).In(zone))
}

func col(label string, width float64, kind string) column {
	align := "right"
	if kind == "text" {
		align = "left"
	}
	return column{label: label, width: width, kind: kind, align: align}
}
func rawColumn() column  { c := col("原始 Token", 19, "token"); c.emphasis = true; return c }
func rankColumn() column { c := col("序号", 8, "number"); c.align = "center"; return c }

func textRowHeight(value string, width float64) float64 {
	lines := 0.0
	for _, line := range strings.Split(value, "\n") {
		length := 0.0
		for _, r := range line {
			length++
			if r > 127 {
				length++
			}
		}
		lines += max(1, math.Ceil(length/max(1, width-2)))
	}
	return min(409, max(29, lines*15+8))
}

func (x *workbook) tableRow(sheet string, row int, columns []column, values []any, total bool) {
	fill := "FFFFFF"
	if row%2 == 0 {
		fill = stripeColor
	}
	if total {
		fill = totalColor
	}
	height := 29.0
	for i, c := range columns {
		style := x.cellStyle(c.kind, fill, c.align, total || c.emphasis)
		if c.kind == "token" {
			style = x.tokenStyle(style, values[i])
		}
		x.put(sheet, cell(i+2, row), values[i], style)
		if c.kind == "text" {
			if value, ok := values[i].(string); ok {
				height = max(height, textRowHeight(value, c.width))
			}
		}
	}
	x.check(x.file.SetRowHeight(sheet, row, height))
}

func (x *workbook) table(sheet string, r Report, note string, columns []column, rows [][]any, total []any, freezeColumns int) {
	lastCol := len(columns) + 1
	x.setup(sheet, r, lastCol, note)
	for i, c := range columns {
		letter := cellColumn(i + 2)
		x.check(x.file.SetColWidth(sheet, letter, letter, c.width))
		x.put(sheet, cell(i+2, tableHeaderRow), c.label, x.cellStyle("header", inkColor, c.align, true))
	}
	x.check(x.file.SetRowHeight(sheet, tableHeaderRow, 30))
	for i, values := range rows {
		x.tableRow(sheet, i+tableDataRow, columns, values, false)
	}
	last := tableHeaderRow + len(rows)
	x.tableRow(sheet, last+1, columns, total, true)
	// Keep the existing native filter support scoped to data, excluding totals.
	if len(rows) > 0 {
		x.check(x.file.AutoFilter(sheet, fmt.Sprintf("B%d:%s", tableHeaderRow, cell(lastCol, last)), nil))
	}
	for i, c := range columns {
		if c.kind != "delta" || len(rows) == 0 {
			continue
		}
		up, err := x.file.NewConditionalStyle(&excelize.Style{Font: &excelize.Font{Color: amberColor}})
		x.check(err)
		down, err := x.file.NewConditionalStyle(&excelize.Style{Font: &excelize.Font{Color: tealColor}})
		x.check(err)
		x.check(x.file.SetConditionalFormat(sheet, fmt.Sprintf("%s:%s", cell(i+2, tableDataRow), cell(i+2, last)), []excelize.ConditionalFormatOptions{
			{Type: "cell", Criteria: ">", Value: "0", Format: &up}, {Type: "cell", Criteria: "<", Value: "0", Format: &down},
		}))
	}
	x.check(x.file.SetPanes(sheet, &excelize.Panes{Freeze: true, XSplit: freezeColumns, YSplit: tableHeaderRow, TopLeftCell: cell(freezeColumns+1, tableDataRow), ActivePane: "bottomRight"}))
	x.check(x.file.SetDefinedName(&excelize.DefinedName{Name: "_xlnm.Print_Titles", RefersTo: fmt.Sprintf("'%s'!$1:$7", sheet), Scope: sheet}))
}

func (x *workbook) details(r Report) {
	m, p := r.Current, r.Previous
	columns := []column{rankColumn(), col("团队", 26, "text"), col("活跃用户", 13, "number"), col("请求数", 15, "number"), rawColumn(), col("加权 Token", 19, "token"), col("用量占比", 14, "percent"), col("上期 Token", 19, "token"), col("环比", 14, "delta"), col("人均 Token", 19, "token")}
	rows := make([][]any, 0, len(r.Teams))
	for i, e := range r.Teams {
		rows = append(rows, []any{i + 1, e.Name, e.Current.Users, e.Current.RequestCount, e.Current.TotalTokens, e.Current.WeightedTokens, ratio(e.Current.TotalTokens, m.TotalTokens), e.Previous.TotalTokens, change(e.Current.TotalTokens, e.Previous.TotalTokens), ratio(e.Current.TotalTokens, int64(e.Current.Users))})
	}
	x.table(teamSheet, r, "按原始 Token 降序；占比、环比和人均以原始 Token 计算。团队按当前归属汇总。", columns, rows, []any{"", "合计", m.Users, m.RequestCount, m.TotalTokens, m.WeightedTokens, ratio(m.TotalTokens, m.TotalTokens), p.TotalTokens, change(m.TotalTokens, p.TotalTokens), ratio(m.TotalTokens, int64(m.Users))}, 3)
	columns = []column{col("账号编号", 20, "text"), col("账号", 32, "text"), col("本周状态", 12, "text"), col("当前绑定", 13, "number"), col("活跃用户", 13, "number"), col("请求数", 15, "number"), col("输入 Token", 19, "token"), col("其中缓存", 19, "token"), col("输出 Token", 19, "token"), rawColumn(), col("加权 Token", 19, "token"), col("用量占比", 14, "percent"), col("上期 Token", 19, "token"), col("环比", 14, "delta"), col("成功率", 13, "percent"), col("最后请求时间", 25, "date")}
	columns[2].align = "center"
	rows = make([][]any, 0, len(r.Accounts))
	bound := 0
	for _, e := range r.Accounts {
		id := e.ID
		if id == "" {
			id = "未识别账号"
		}
		bound += e.BoundUsers
		c := e.Current
		rows = append(rows, []any{id, e.Name, activity(c), e.BoundUsers, c.Users, c.RequestCount, c.InputTokens, c.CachedTokens, c.OutputTokens, c.TotalTokens, c.WeightedTokens, ratio(c.TotalTokens, m.TotalTokens), e.Previous.TotalTokens, change(c.TotalTokens, e.Previous.TotalTokens), ratio(c.SuccessCount, c.RequestCount), lastUsed(c, r.Period.Start.Location())})
	}
	x.table(accountSheet, r, "按原始 Token 降序。当前绑定为导出时路由；用量按请求实际账号统计。缓存包含在输入中，活跃用户合计去重。", columns, rows, []any{"", "合计", "", bound, m.Users, m.RequestCount, m.InputTokens, m.CachedTokens, m.OutputTokens, m.TotalTokens, m.WeightedTokens, ratio(m.TotalTokens, m.TotalTokens), p.TotalTokens, change(m.TotalTokens, p.TotalTokens), ratio(m.SuccessCount, m.RequestCount), lastUsed(m, r.Period.Start.Location())}, 3)
	columns = []column{col("用户", 34, "text"), col("当前团队", 22, "text"), col("当前绑定账号", 20, "text"), col("使用账号数", 14, "number"), col("请求数", 15, "number"), col("输入 Token", 19, "token"), col("其中缓存", 19, "token"), col("输出 Token", 19, "token"), rawColumn(), col("加权 Token", 19, "token"), col("用量占比", 14, "percent"), col("上期 Token", 19, "token"), col("环比", 14, "delta"), col("活跃天数", 13, "number"), col("最后请求时间", 25, "date")}
	rows = make([][]any, 0, len(r.Users))
	for _, e := range r.Users {
		c := e.Current
		rows = append(rows, []any{e.Name, e.Team, e.CurrentAccount, c.Accounts, c.RequestCount, c.InputTokens, c.CachedTokens, c.OutputTokens, c.TotalTokens, c.WeightedTokens, ratio(c.TotalTokens, m.TotalTokens), e.Previous.TotalTokens, change(c.TotalTokens, e.Previous.TotalTokens), c.Days, lastUsed(c, r.Period.Start.Location())})
	}
	x.table(userSheet, r, "按原始 Token 降序。缓存包含在输入中；原始和加权值均保留历史记录。使用账号数、活跃天数合计去重。", columns, rows, []any{"合计", "", "", m.Accounts, m.RequestCount, m.InputTokens, m.CachedTokens, m.OutputTokens, m.TotalTokens, m.WeightedTokens, ratio(m.TotalTokens, m.TotalTokens), p.TotalTokens, change(m.TotalTokens, p.TotalTokens), m.Days, lastUsed(m, r.Period.Start.Location())}, 3)
}

func (x *workbook) daily(r Report) {
	columns := []column{col("日期", 17, "day"), col("星期", 10, "text"), rawColumn(), col("加权 Token", 19, "token"), col("上期日期", 17, "day"), col("上期 Token", 19, "token"), col("上期加权 Token", 21, "token"), col("环比", 14, "delta"), col("请求数", 15, "number"), col("活跃账号", 13, "number"), col("活跃团队", 13, "number"), col("活跃用户", 13, "number"), col("上期记录", 17, "text")}
	weekdays := []string{"周一", "周二", "周三", "周四", "周五", "周六", "周日"}
	rows := make([][]any, 0, len(r.Daily))
	for i, d := range r.Daily {
		values := []any{localExcelTime(d.Date), weekdays[i], nil, nil, localExcelTime(d.PreviousDate), nil, nil, nil, nil, nil, nil, nil, "未到统计时间"}
		if d.Included {
			c := d.Current
			p := d.Previous
			values = []any{localExcelTime(d.Date), weekdays[i], c.TotalTokens, c.WeightedTokens, localExcelTime(d.PreviousDate), p.TotalTokens, p.WeightedTokens, change(c.TotalTokens, p.TotalTokens), c.RequestCount, c.Accounts, c.Teams, c.Users, comparisonStatus(p)}
		}
		rows = append(rows, values)
	}
	m, p := r.Current, r.Previous
	x.table(dailySheet, r, "日期按系统时区划分，上期按星期对齐。未发生的日期留空；活跃人数与账号数合计去重。", columns, rows, []any{"合计（去重）", "", m.TotalTokens, m.WeightedTokens, "", p.TotalTokens, p.WeightedTokens, change(m.TotalTokens, p.TotalTokens), m.RequestCount, m.Accounts, m.Teams, m.Users, comparisonStatus(p)}, 3)
}
