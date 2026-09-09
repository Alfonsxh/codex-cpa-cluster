package usagereport

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/xuri/excelize/v2"
)

const ContentType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"

type XLSXOptions struct {
	WithUnits bool
}

type tokenStyleKey struct {
	base   int
	format string
}

type workbook struct {
	file                                                     *excelize.File
	ctx                                                      context.Context
	err                                                      error
	title, header, text, number, percent, delta, total, note int
	withUnits                                                bool
	tokenStyles                                              map[tokenStyleKey]int
}

// XLSX returns an entirely generated workbook before HTTP headers are written.
// SetCellValue writes labels as strings, never formulas or hyperlinks.
func XLSX(ctx context.Context, report Report, options ...XLSXOptions) ([]byte, error) {
	x := &workbook{file: excelize.NewFile(), ctx: ctx, tokenStyles: map[tokenStyleKey]int{}}
	if len(options) > 0 {
		x.withUnits = options[0].WithUnits
	}
	defer x.file.Close()
	x.check(x.file.SetSheetName("Sheet1", "周报总览"))
	for _, name := range []string{"团队统计", "账号统计", "个人统计", "每日趋势"} {
		_, err := x.file.NewSheet(name)
		x.check(err)
	}
	x.title = x.style(&excelize.Style{Font: &excelize.Font{Family: "Microsoft YaHei", Size: 20, Bold: true, Color: "FFFFFF"}, Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"172B4D"}}, Alignment: &excelize.Alignment{Vertical: "center", Indent: 1}})
	x.header = x.style(&excelize.Style{Font: &excelize.Font{Family: "Microsoft YaHei", Size: 11, Bold: true, Color: "FFFFFF"}, Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"34567A"}}, Alignment: &excelize.Alignment{Vertical: "center", WrapText: true, Indent: 1}})
	x.text = x.style(&excelize.Style{Font: &excelize.Font{Family: "Microsoft YaHei", Size: 11, Color: "243752"}, Alignment: &excelize.Alignment{Vertical: "center", Indent: 1}})
	x.number = x.style(&excelize.Style{Font: &excelize.Font{Family: "Microsoft YaHei", Size: 11, Color: "243752"}, NumFmt: 3, Alignment: &excelize.Alignment{Vertical: "center", Horizontal: "right", Indent: 1}})
	x.percent = x.style(&excelize.Style{NumFmt: 10, Font: &excelize.Font{Family: "Microsoft YaHei", Size: 11, Color: "243752"}, Alignment: &excelize.Alignment{Vertical: "center", Horizontal: "right", Indent: 1}})
	deltaFormat := `+0.0%;-0.0%;0.0%`
	x.delta = x.style(&excelize.Style{CustomNumFmt: &deltaFormat, Font: &excelize.Font{Family: "Microsoft YaHei", Size: 11, Color: "243752"}, Alignment: &excelize.Alignment{Vertical: "center", Horizontal: "right", Indent: 1}})
	x.total = x.style(&excelize.Style{NumFmt: 3, Font: &excelize.Font{Family: "Microsoft YaHei", Size: 11, Bold: true, Color: "172B4D"}, Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"E8EFF7"}}, Alignment: &excelize.Alignment{Vertical: "center", Indent: 1}})
	x.note = x.style(&excelize.Style{Font: &excelize.Font{Family: "Microsoft YaHei", Size: 10, Color: "596B80"}, Alignment: &excelize.Alignment{Vertical: "center", WrapText: true, Indent: 1}})
	x.details(report)
	x.daily(report)
	x.summary(report)
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

func cell(col, row int) string {
	name, _ := excelize.CoordinatesToCellName(col, row)
	return name
}

func (x *workbook) row(sheet string, row int, values []any, style int, tokenColumns ...int) {
	if x.err != nil {
		return
	}
	x.check(x.file.SetSheetRow(sheet, cell(1, row), &values))
	x.check(x.file.SetCellStyle(sheet, cell(1, row), cell(len(values), row), style))
	if x.withUnits {
		for _, col := range tokenColumns {
			var amount float64
			switch value := values[col-1].(type) {
			case int64:
				amount = float64(value)
			case float64:
				amount = value
			default:
				continue
			}
			tokenStyle := x.tokenStyle(style, amount)
			if x.err != nil {
				return
			}
			x.check(x.file.SetCellStyle(sheet, cell(col, row), cell(col, row), tokenStyle))
		}
	}
	x.check(x.file.SetRowHeight(sheet, row, 26))
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

func (x *workbook) tokenStyle(base int, amount float64) int {
	key := tokenStyleKey{base: base, format: compactTokenNumberFormat(amount)}
	if id, ok := x.tokenStyles[key]; ok {
		return id
	}
	style, err := x.file.GetStyle(base)
	x.check(err)
	if x.err != nil {
		return base
	}
	// Change only Excel's display format; retain the numeric cell value and
	// the original row's font, alignment and total-row background.
	style.NumFmt = 0
	style.CustomNumFmt = &key.format
	id := x.style(style)
	x.tokenStyles[key] = id
	return id
}

func (x *workbook) merged(sheet, from, to string, value any, style int) {
	if x.err != nil {
		return
	}
	x.check(x.file.MergeCell(sheet, from, to))
	x.check(x.file.SetCellValue(sheet, from, value))
	x.check(x.file.SetCellStyle(sheet, from, to, style))
}

func (x *workbook) setup(sheet string, r Report, columns int, note string) {
	x.merged(sheet, "A1", cell(columns, 1), "CCPA · "+sheet, x.title)
	x.check(x.file.SetRowHeight(sheet, 1, 44))
	state := "完整自然周"
	if r.Period.Partial() {
		state = "本周未结束 · 截至导出时间"
	}
	meta := fmt.Sprintf("%s — %s（结束不含）｜%s｜%s", r.Period.Start.Format(time.DateTime), r.Period.End.Format(time.DateTime), r.Period.Start.Location(), state)
	x.merged(sheet, "A2", cell(columns, 2), meta, x.note)
	x.check(x.file.SetRowHeight(sheet, 2, 30))
	x.merged(sheet, "A3", cell(columns, 3), note, x.note)
	x.check(x.file.SetRowHeight(sheet, 3, 30))
	lastCol, _ := excelize.ColumnNumberToName(columns)
	x.check(x.file.SetColWidth(sheet, "A", lastCol, 18))
	showGrid := false
	x.check(x.file.SetSheetView(sheet, 0, &excelize.ViewOptions{ShowGridLines: &showGrid}))
	x.check(x.file.SetPageLayout(sheet, &excelize.PageLayoutOptions{Orientation: ptr("landscape"), Size: ptr(9), FitToWidth: ptr(1), FitToHeight: ptr(0)}))
}

func ptr[T any](value T) *T { return &value }

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

func (x *workbook) finishTable(sheet string, last, freezeColumns int, percents []int, delta int, weightedColumn int) {
	if x.err != nil {
		return
	}
	for _, col := range percents {
		x.check(x.file.SetCellStyle(sheet, cell(col, 6), cell(col, last+2), x.percent))
	}
	if delta > 0 {
		x.check(x.file.SetCellStyle(sheet, cell(delta, 6), cell(delta, last+2), x.delta))
	}
	if last >= 6 {
		x.check(x.file.AutoFilter(sheet, fmt.Sprintf("A5:L%d", last), nil))
		stripe, err := x.file.NewConditionalStyle(&excelize.Style{Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"F3F6FA"}}})
		x.check(err)
		x.check(x.file.SetConditionalFormat(sheet, fmt.Sprintf("A6:L%d", last), []excelize.ConditionalFormatOptions{{Type: "formula", Criteria: "MOD(ROW(),2)=0", Format: &stripe}}))
		if weightedColumn > 0 {
			x.check(x.file.SetConditionalFormat(sheet, fmt.Sprintf("%s:%s", cell(weightedColumn, 6), cell(weightedColumn, last)), []excelize.ConditionalFormatOptions{{Type: "data_bar", Criteria: "=", MinType: "num", MinValue: "0", MaxType: "max", BarColor: "#8DAFDB"}}))
		}
	}
	x.check(x.file.SetPanes(sheet, &excelize.Panes{Freeze: true, XSplit: freezeColumns, YSplit: 5, TopLeftCell: cell(freezeColumns+1, 6), ActivePane: "bottomRight"}))
	x.check(x.file.SetDefinedName(&excelize.DefinedName{Name: "_xlnm.Print_Titles", RefersTo: fmt.Sprintf("'%s'!$1:$5", sheet), Scope: sheet}))
}

func (x *workbook) details(r Report) {
	for _, sheet := range []string{"团队统计", "账号统计", "个人统计"} {
		x.setup(sheet, r, 12, "按加权 Token 降序；含当前及历史身份。团队按当前归属；上期为前一周同一时段。人数合计为去重值。")
		x.check(x.file.SetColWidth(sheet, "A", "A", 8))
		x.check(x.file.SetColWidth(sheet, "B", "B", 32))
	}
	x.row("团队统计", 5, []any{"排名", "团队", "活跃人数", "原始 Token", "加权 Token", "消耗占比", "人均加权 Token", "上期加权 Token", "环比", "请求次数", "成功率", "上期记录"}, x.header)
	for i, e := range r.Teams {
		x.row("团队统计", i+6, []any{i + 1, e.Name, e.Current.Users, e.Current.TotalTokens, e.Current.WeightedTokens, ratio(e.Current.WeightedTokens, r.Current.WeightedTokens), ratio(e.Current.WeightedTokens, int64(e.Current.Users)), e.Previous.WeightedTokens, change(e.Current.WeightedTokens, e.Previous.WeightedTokens), e.Current.RequestCount, ratio(e.Current.SuccessCount, e.Current.RequestCount), comparisonStatus(e.Previous)}, x.number, 4, 5, 7, 8)
		x.check(x.file.SetCellStyle("团队统计", cell(2, i+6), cell(2, i+6), x.text))
	}
	m, p := r.Current, r.Previous
	x.row("团队统计", len(r.Teams)+7, []any{"合计", "全部团队及未分配", m.Users, m.TotalTokens, m.WeightedTokens, ratio(m.WeightedTokens, m.WeightedTokens), ratio(m.WeightedTokens, int64(m.Users)), p.WeightedTokens, change(m.WeightedTokens, p.WeightedTokens), m.RequestCount, ratio(m.SuccessCount, m.RequestCount), comparisonStatus(p)}, x.total, 4, 5, 7, 8)
	x.finishTable("团队统计", len(r.Teams)+5, 2, []int{6, 11}, 9, 5)
	x.row("账号统计", 5, []any{"排名", "CPA 账号", "账号标识", "使用人数", "原始 Token", "加权 Token", "消耗占比", "上期加权 Token", "环比", "请求次数", "成功率", "上期记录"}, x.header)
	x.check(x.file.SetColWidth("账号统计", "C", "C", 36))
	for i, e := range r.Accounts {
		id := e.ID
		if id == "" {
			id = "未识别账号"
		}
		x.row("账号统计", i+6, []any{i + 1, id, e.Name, e.Current.Users, e.Current.TotalTokens, e.Current.WeightedTokens, ratio(e.Current.WeightedTokens, m.WeightedTokens), e.Previous.WeightedTokens, change(e.Current.WeightedTokens, e.Previous.WeightedTokens), e.Current.RequestCount, ratio(e.Current.SuccessCount, e.Current.RequestCount), comparisonStatus(e.Previous)}, x.number, 5, 6, 8)
		x.check(x.file.SetCellStyle("账号统计", cell(2, i+6), cell(3, i+6), x.text))
	}
	x.row("账号统计", len(r.Accounts)+7, []any{"合计", "全部账号", "", m.Users, m.TotalTokens, m.WeightedTokens, ratio(m.WeightedTokens, m.WeightedTokens), p.WeightedTokens, change(m.WeightedTokens, p.WeightedTokens), m.RequestCount, ratio(m.SuccessCount, m.RequestCount), comparisonStatus(p)}, x.total, 5, 6, 8)
	x.finishTable("账号统计", len(r.Accounts)+5, 3, []int{7, 11}, 9, 6)
	x.row("个人统计", 5, []any{"排名", "用户", "当前团队", "使用账号数", "活跃天数", "原始 Token", "加权 Token", "消耗占比", "上期加权 Token", "环比", "请求次数", "上期记录"}, x.header)
	x.check(x.file.SetColWidth("个人统计", "B", "B", 36))
	x.check(x.file.SetColWidth("个人统计", "C", "C", 28))
	for i, e := range r.Users {
		x.row("个人统计", i+6, []any{i + 1, e.Name, e.Team, e.Current.Accounts, e.Current.Days, e.Current.TotalTokens, e.Current.WeightedTokens, ratio(e.Current.WeightedTokens, m.WeightedTokens), e.Previous.WeightedTokens, change(e.Current.WeightedTokens, e.Previous.WeightedTokens), e.Current.RequestCount, comparisonStatus(e.Previous)}, x.number, 6, 7, 9)
		x.check(x.file.SetCellStyle("个人统计", cell(2, i+6), cell(3, i+6), x.text))
	}
	x.row("个人统计", len(r.Users)+7, []any{"合计", "全部用户", "", m.Accounts, m.Days, m.TotalTokens, m.WeightedTokens, ratio(m.WeightedTokens, m.WeightedTokens), p.WeightedTokens, change(m.WeightedTokens, p.WeightedTokens), m.RequestCount, comparisonStatus(p)}, x.total, 6, 7, 9)
	x.finishTable("个人统计", len(r.Users)+5, 3, []int{8}, 10, 7)
}

func (x *workbook) daily(r Report) {
	const sheet = "每日趋势"
	x.setup(sheet, r, 12, "日期按系统时区划分；上期按星期对齐。本周尚未发生的日期留空，今日及上周对应日仅统计相同时段。")
	x.row(sheet, 5, []any{"日期", "星期", "原始 Token", "加权 Token", "上期日期", "上期加权 Token", "环比", "请求次数", "活跃账号", "活跃团队", "活跃人数", "上期记录"}, x.header)
	weekdays := []string{"周一", "周二", "周三", "周四", "周五", "周六", "周日"}
	for i, d := range r.Daily {
		values := []any{d.Date.Format(time.DateOnly), weekdays[i], nil, nil, d.PreviousDate.Format(time.DateOnly), nil, nil, nil, nil, nil, nil, "未到统计时间"}
		if d.Included {
			values = []any{d.Date.Format(time.DateOnly), weekdays[i], d.Current.TotalTokens, d.Current.WeightedTokens, d.PreviousDate.Format(time.DateOnly), d.Previous.WeightedTokens, change(d.Current.WeightedTokens, d.Previous.WeightedTokens), d.Current.RequestCount, d.Current.Accounts, d.Current.Teams, d.Current.Users, comparisonStatus(d.Previous)}
		}
		x.row(sheet, i+6, values, x.number, 3, 4, 6)
	}
	m, p := r.Current, r.Previous
	x.row(sheet, 14, []any{"合计（去重）", "", m.TotalTokens, m.WeightedTokens, "", p.WeightedTokens, change(m.WeightedTokens, p.WeightedTokens), m.RequestCount, m.Accounts, m.Teams, m.Users, comparisonStatus(p)}, x.total, 3, 4, 6)
	x.finishTable(sheet, 12, 2, nil, 7, 4)
}

func (x *workbook) summary(r Report) {
	const sheet = "周报总览"
	x.setup(sheet, r, 8, "导出时间："+r.Period.GeneratedAt.Format(time.DateTime)+"｜上期："+r.Period.PreviousStart.Format(time.DateTime)+" — "+r.Period.PreviousEnd.Format(time.DateTime)+"（结束不含）")
	x.check(x.file.SetColWidth(sheet, "A", "A", 27))
	x.check(x.file.SetColWidth(sheet, "B", "D", 23))
	x.check(x.file.SetColWidth(sheet, "E", "H", 14))
	x.row(sheet, 5, []any{"核心指标", "本期", "上期", "环比／变化"}, x.header)
	m, p := r.Current, r.Previous
	metrics := [][]any{
		{"加权 Token", m.WeightedTokens, p.WeightedTokens, change(m.WeightedTokens, p.WeightedTokens)},
		{"原始 Token", m.TotalTokens, p.TotalTokens, change(m.TotalTokens, p.TotalTokens)},
		{"请求次数", m.RequestCount, p.RequestCount, change(m.RequestCount, p.RequestCount)},
		{"活跃 CPA 账号", m.Accounts, p.Accounts, m.Accounts - p.Accounts},
		{"活跃团队", m.Teams, p.Teams, m.Teams - p.Teams},
		{"活跃人数", m.Users, p.Users, m.Users - p.Users},
	}
	for i, values := range metrics {
		var tokenColumns []int
		if i < 2 {
			tokenColumns = []int{2, 3}
		}
		x.row(sheet, i+6, values, x.number, tokenColumns...)
		x.check(x.file.SetCellStyle(sheet, cell(1, i+6), cell(1, i+6), x.text))
	}
	x.check(x.file.SetCellStyle(sheet, "D6", "D8", x.delta))
	status := "本期与上期均有请求记录"
	if p.RequestCount == 0 {
		status = "上期无请求记录，环比不可计算"
	}
	if m.RequestCount == 0 {
		status = "本期无请求记录；零值仅表示未采集到用量"
	}
	x.merged(sheet, "E5", "H5", "数据状态", x.header)
	x.merged(sheet, "E6", "H8", status, x.note)
	x.merged(sheet, "E9", "H11", "消耗增长仅反映变化，不自动判定为异常。详细数据见后四张表。", x.note)
	x.merged(sheet, "A13", "H13", "每日加权 Token · 本期与上期同星期对比", x.header)
	x.check(x.file.SetRowHeight(sheet, 13, 28))
	for row := 14; row <= 28; row++ {
		x.check(x.file.SetRowHeight(sheet, row, 22))
	}
	chartNumberFormat := "#,##0"
	if x.withUnits {
		var peak int64
		for _, day := range r.Daily {
			peak = max(peak, day.Current.WeightedTokens, day.Previous.WeightedTokens)
		}
		chartNumberFormat = compactTokenNumberFormat(float64(peak))
	}
	x.check(x.file.AddChart(sheet, "A14", &excelize.Chart{
		Type: excelize.Line,
		Series: []excelize.ChartSeries{
			{Name: "'每日趋势'!$D$5", Categories: "'每日趋势'!$B$6:$B$12", Values: "'每日趋势'!$D$6:$D$12"},
			{Name: "'每日趋势'!$F$5", Categories: "'每日趋势'!$B$6:$B$12", Values: "'每日趋势'!$F$6:$F$12"},
		},
		Dimension:    excelize.ChartDimension{Width: 1040, Height: 405},
		Legend:       excelize.ChartLegend{Position: "bottom"},
		YAxis:        excelize.ChartAxis{MajorGridLines: true, NumFmt: excelize.ChartNumFmt{CustomNumFmt: chartNumberFormat}},
		ShowBlanksAs: "gap",
	}))
	row := 30
	for _, group := range []struct {
		title   string
		entries []Entry
		account bool
	}{
		{"团队消耗 Top5", r.Teams, false}, {"账号消耗 Top5", r.Accounts, true}, {"个人消耗 Top5", r.Users, false},
	} {
		x.merged(sheet, cell(1, row), cell(8, row), group.title, x.header)
		x.row(sheet, row+1, []any{"名称", "加权 Token", "占比", "环比"}, x.header)
		count := 0
		for _, e := range group.entries {
			if count == 5 {
				break
			}
			if e.Current.RequestCount == 0 {
				continue
			}
			name := e.Name
			if group.account {
				name = e.ID
				if name == "" {
					name = "未识别账号"
				}
			}
			x.row(sheet, row+2+count, []any{name, e.Current.WeightedTokens, ratio(e.Current.WeightedTokens, m.WeightedTokens), change(e.Current.WeightedTokens, e.Previous.WeightedTokens)}, x.number, 2)
			x.check(x.file.SetCellStyle(sheet, cell(3, row+2+count), cell(3, row+2+count), x.percent))
			x.check(x.file.SetCellStyle(sheet, cell(4, row+2+count), cell(4, row+2+count), x.delta))
			// Give long identifiers enough room without overlapping numeric columns.
			x.check(x.file.SetCellStyle(sheet, cell(1, row+2+count), cell(1, row+2+count), x.note))
			x.check(x.file.SetRowHeight(sheet, row+2+count, 38))
			count++
		}
		if count == 0 {
			x.merged(sheet, cell(1, row+2), cell(4, row+2), "本期无请求记录", x.note)
		}
		row += 9
	}
	x.merged(sheet, cell(1, row), cell(8, row), "统计口径", x.header)
	notes := []string{
		"加权 Token 使用请求采集时保存的结果；历史记录不按当前倍率重算。旧版无加权值的记录沿用原始 Token。",
		"占比、排名、人均和环比以加权 Token 为准；人均按活跃人数计算。原始 Token 为采集记录的 total_tokens。",
		"团队按导出时的当前归属统计，两期使用同一关系；未匹配的历史用户计入未分配，历史账号仍保留。",
		"活跃指期间有请求。人数与账号数均去重；活跃团队不含未分配，空身份不计入活跃人数／账号数，但用量仍计入合计。",
		"上期零值或无请求记录时环比为 —；无记录不保证没有实际用量。采集延迟或缺失会影响本表。",
		"数据范围覆盖全部账号及用户，不受页面搜索、筛选和 Top10 限制。当前身份无请求也保留在明细中。",
	}
	if x.withUnits {
		notes = append(notes, "Token 按 K/M/B 单位显示；单元格保留完整数值，可继续求和、排序与统计。")
	} else {
		notes = append(notes, "Token 以完整数值显示，不附加单位；单元格可继续求和、排序与统计。")
	}
	for i, note := range notes {
		x.merged(sheet, cell(1, row+1+i), cell(8, row+1+i), note, x.note)
		x.check(x.file.SetRowHeight(sheet, row+1+i, 34))
	}
	x.check(x.file.SetPanes(sheet, &excelize.Panes{Freeze: true, YSplit: 3, TopLeftCell: "A4", ActivePane: "bottomLeft"}))
}
