package notifications

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Alfonsxh/codex-cpa-pool/internal/quota"
)

func UsageCenterURL(publicBaseURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(publicBaseURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return ""
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	if !strings.HasSuffix(parsed.Path, "/usage") {
		parsed.Path += "/usage"
	}
	parsed.Path += "/"
	parsed.RawPath, parsed.RawQuery, parsed.Fragment = "", "", ""
	parsed.ForceQuery = false
	return parsed.String()
}

// Reports and alerts share one regular weekly window per account. Additional
// model-specific windows never become extra accounts or independent alerts.
func QuotaRows(snapshot Snapshot, thresholdPercent float64, onlyKeys map[string]struct{}) []Row {
	rows := make([]Row, 0, len(snapshot.Accounts))
	seen := make(map[string]struct{}, len(snapshot.Accounts))
	for _, account := range snapshot.Accounts {
		id := defaultString(strings.TrimSpace(account.ID), "unknown")
		if _, found := seen[id]; found {
			continue
		}
		seen[id] = struct{}{}
		row := Row{
			Key: id + "|unavailable", Account: id, Label: "常规周限额",
			ActiveUsers: max(0, account.ActiveUsers1H), Level: "unavailable",
		}
		if account.Quota.Status == "ok" {
			row.ResetCount = account.Quota.ResetCreditCount
		}
		if window, found := regularWeeklyWindow(account.Quota); found {
			row.Key = id + "|" + window.Key
			if account.Quota.Status == "ok" && !math.IsNaN(window.UsedPercent) && !math.IsInf(window.UsedPercent, 0) {
				used := math.Max(0, math.Min(window.UsedPercent, 100))
				row.Level = "normal"
				switch {
				case window.LimitReached || used >= 100:
					used, row.Level = 100, "exhausted"
				case used >= thresholdPercent:
					row.Level = "warning"
				}
				row.UsedPercent, row.ResetAt, row.ResetKey = &used, window.ResetAt, window.ResetAt
			}
		}
		if len(onlyKeys) > 0 {
			if _, found := onlyKeys[row.Key]; !found {
				continue
			}
		}
		rows = append(rows, row)
	}
	priority := map[string]int{"exhausted": 0, "warning": 1, "unavailable": 2, "normal": 3}
	sort.SliceStable(rows, func(left int, right int) bool {
		if priority[rows[left].Level] != priority[rows[right].Level] {
			return priority[rows[left].Level] < priority[rows[right].Level]
		}
		if rows[left].UsedPercent != nil && rows[right].UsedPercent != nil && *rows[left].UsedPercent != *rows[right].UsedPercent {
			return *rows[left].UsedPercent > *rows[right].UsedPercent
		}
		return naturalCompare(rows[left].Account, rows[right].Account) < 0
	})
	return rows
}

func regularWeeklyWindow(accountQuota quota.AccountQuota) (quota.WeeklyWindow, bool) {
	windows := accountQuota.WeeklyWindows
	if len(windows) == 0 && accountQuota.Weekly != nil {
		windows = []quota.WeeklyWindow{*accountQuota.Weekly}
	}
	var selected quota.WeeklyWindow
	found := false
	for _, window := range windows {
		key := strings.ToLower(strings.TrimSpace(window.Key))
		if key == "" {
			key = "default:primary_window"
		}
		if !strings.HasPrefix(key, "default:") || strings.Contains(strings.ToLower(window.Label), "gpt-5.3") ||
			(window.WindowSeconds != 0 && window.WindowSeconds != quota.WeeklyWindowSeconds) {
			continue
		}
		window.Key = key
		if !found || key == "default:primary_window" {
			selected, found = window, true
		}
		if key == "default:primary_window" {
			break
		}
	}
	return selected, found
}

type ReportOptions struct {
	PreviousWindows map[string]WindowRecord
}

var transitionLabels = map[string]string{
	"warning": "🟠 达到预警", "exhausted": "🔴 额度耗尽",
	"recovered": "🟢 额度恢复", "recovered_warning": "🟠 额度恢复，仍处于预警范围",
	"refreshed": "🔄 周额度已重置",
}

func BuildMarkdownV2(
	snapshot Snapshot,
	title string,
	location *time.Location,
	thresholdPercent float64,
	now time.Time,
	onlyKeys map[string]struct{},
	transitionEvents map[string]string,
	usageCenterURL string,
	options ...ReportOptions,
) (string, error) {
	if location == nil {
		location = time.UTC
	}
	allRows := QuotaRows(snapshot, thresholdPercent, nil)
	rows := allRows
	if len(onlyKeys) > 0 {
		rows = make([]Row, 0, len(onlyKeys))
		for _, row := range allRows {
			if _, found := onlyKeys[row.Key]; found {
				rows = append(rows, row)
			}
		}
	}
	sections := messageHeader(title, location, now, usageCenterURL, thresholdPercent)
	sections = append(sections, accountSummary(allRows))
	if len(onlyKeys) > 0 {
		sections = append(sections, fmt.Sprintf("> 本次涉及：**%d 个账号**", len(rows)))
	}
	if len(onlyKeys) > 0 && len(rows) == 1 {
		var previous map[string]WindowRecord
		if len(options) > 0 {
			previous = options[0].PreviousWindows
		}
		sections = append(sections, accountTransition(rows[0], transitionEvents[rows[0].Key], previous, location, now))
	} else {
		sections = append(sections, accountTable(rows, transitionEvents, location, now, len(onlyKeys) > 0))
	}
	return boundedMessage(sections)
}

func messageHeader(title string, location *time.Location, now time.Time, usageCenterURL string, threshold ...float64) []string {
	sections := []string{"# " + safeCell(title, 64), "> 统计时间：" + notificationTime(now, location)}
	if len(threshold) > 0 {
		sections = append(sections, fmt.Sprintf("> 预警阈值：%s", formatPercentValue(threshold[0])))
	}
	if usageCenterURL = strings.TrimSpace(usageCenterURL); usageCenterURL != "" {
		link := strings.NewReplacer("(", "%28", ")", "%29", "[", "%5B", "]", "%5D").Replace(usageCenterURL)
		sections = append(sections, fmt.Sprintf("> 应用地址：[%s](%s)", link, link))
	}
	return sections
}

func notificationTime(now time.Time, location *time.Location) string {
	if location == nil {
		location = time.UTC
	}
	label := location.String()
	if label == "Asia/Shanghai" {
		label = "北京时间"
	}
	return now.In(location).Format("2006-01-02 15:04:05") + "（" + label + "）"
}

func accountSummary(rows []Row) string {
	counts := make(map[string]int)
	active := 0
	for _, row := range rows {
		counts[row.Level]++
		if row.ActiveUsers > 0 {
			active++
		}
	}
	return fmt.Sprintf("> **账号总数 %d**　近 1 小时活跃账号 %d\n> 🟢 额度正常 %d　🟠 预警 %d　🔴 耗尽 %d　⚪ 数据不可用 %d",
		len(rows), active, counts["normal"], counts["warning"], counts["exhausted"], counts["unavailable"])
}

func accountTable(rows []Row, transitions map[string]string, location *time.Location, now time.Time, eventsOnly bool) string {
	icons := map[string]string{"normal": "🟢", "warning": "🟠", "exhausted": "🔴", "unavailable": "⚪"}
	table := []string{
		"| 账号 | 周额度已用 | 近1h用户 | 剩余重置次数 | 额度重置时间 |",
		"| :--- | ---: | ---: | ---: | :--- |",
	}
	if len(transitions) > 0 {
		table = []string{
			"| 账号 | 变化 | 周额度已用 | 近1h用户 | 剩余重置次数 | 额度重置时间 |",
			"| :--- | :--- | ---: | ---: | ---: | :--- |",
		}
	}
	if eventsOnly {
		table = []string{"| 账号 | 变化 | 当前已用 |", "| :--- | :--- | ---: |"}
	}
	for _, row := range rows {
		if eventsOnly {
			table = append(table, "| "+strings.Join([]string{safeCell(row.Account, 32),
				defaultString(transitionLabels[transitions[row.Key]], "—"), formatPercent(row.UsedPercent)}, " | ")+" |")
			continue
		}
		cells := []string{icons[row.Level] + " " + safeCell(row.Account, 32)}
		if len(transitions) > 0 {
			cells = append(cells, defaultString(transitionLabels[transitions[row.Key]], "—"))
		}
		cells = append(cells, formatPercent(row.UsedPercent), strconv.Itoa(row.ActiveUsers),
			formatOptionalInt(row.ResetCount), formatReset(row.ResetAt, location, now))
		table = append(table, "| "+strings.Join(cells, " | ")+" |")
	}
	if len(rows) == 0 {
		if eventsOnly {
			table = append(table, "| 暂无匹配账号 | — | — |")
		} else if len(transitions) > 0 {
			table = append(table, "| 暂无匹配账号 | — | — | — | — | — |")
		} else {
			table = append(table, "| 暂无匹配账号 | — | — | — | — |")
		}
	}
	return strings.Join(table, "\n")
}

func accountTransition(row Row, event string, previous map[string]WindowRecord, location *time.Location, now time.Time) string {
	used := formatPercent(row.UsedPercent)
	if before, found := previous[row.Key]; found && row.UsedPercent != nil &&
		!math.IsNaN(before.UsedPercent) && !math.IsInf(before.UsedPercent, 0) && before.UsedPercent != *row.UsedPercent {
		used = formatPercentValue(before.UsedPercent) + " → " + used
	}
	remaining := "—"
	if row.UsedPercent != nil {
		remaining = formatPercentValue(100 - *row.UsedPercent)
	}
	return strings.Join([]string{
		"**" + safeCell(row.Account, 32) + "** · " + defaultString(transitionLabels[event], "额度状态变更"),
		"周额度已用：" + used + "　当前剩余：" + remaining,
		fmt.Sprintf("近 1 小时用户：%d", row.ActiveUsers),
		"剩余重置次数：" + formatOptionalInt(row.ResetCount),
		"额度重置时间：" + formatReset(row.ResetAt, location, now),
	}, "\n\n")
}

func BuildTestMarkdownV2(config Config, now time.Time) (string, error) {
	sections := messageHeader("✅ "+config.ShortName+" · 通知测试", config.Timezone, now, UsageCenterURL(config.PublicBaseURL))
	sections = append(sections, "企业微信通知通道连接正常。", "> 消息类型：通道测试")
	return boundedMessage(sections)
}

func boundedMessage(sections []string) (string, error) {
	content := strings.Join(sections, "\n\n")
	if len([]byte(content)) > MarkdownV2MaximumSize {
		return "", errors.New("企业微信 markdown_v2 内容超过 4096 字节")
	}
	return content, nil
}

func PayloadHash(content string) string {
	digest := sha256.Sum256([]byte(content))
	return hex.EncodeToString(digest[:])
}

func safeCell(value string, limit int) string {
	text := strings.TrimSpace(strings.NewReplacer("|", `\|`, "\r", " ", "\n", " ").Replace(value))
	if text == "" {
		return "—"
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:max(1, limit-1)]) + "…"
}

func formatPercent(value *float64) string {
	if value == nil {
		return "—"
	}
	return formatPercentValue(*value)
}

func formatPercentValue(value float64) string {
	rendered := strings.TrimRight(strings.TrimRight(strconv.FormatFloat(value, 'f', 2, 64), "0"), ".")
	return rendered + "%"
}

func formatOptionalInt(value *int64) string {
	if value == nil {
		return "—"
	}
	return strconv.FormatInt(*value, 10)
}

func formatReset(timestamp *int64, location *time.Location, now time.Time) string {
	if timestamp == nil || *timestamp <= 0 {
		return "—"
	}
	if *timestamp <= now.Unix() {
		return "等待额度更新"
	}
	return time.Unix(*timestamp, 0).In(location).Format("01-02 15:04")
}

func defaultString(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func naturalCompare(left string, right string) int {
	leftParts := naturalParts(left)
	rightParts := naturalParts(right)
	for index := 0; index < min(len(leftParts), len(rightParts)); index++ {
		leftPart, rightPart := leftParts[index], rightParts[index]
		if leftPart.number && rightPart.number {
			leftNumber, _ := strconv.ParseUint(leftPart.value, 10, 64)
			rightNumber, _ := strconv.ParseUint(rightPart.value, 10, 64)
			if leftNumber < rightNumber {
				return -1
			}
			if leftNumber > rightNumber {
				return 1
			}
		} else {
			leftValue, rightValue := strings.ToLower(leftPart.value), strings.ToLower(rightPart.value)
			if leftValue < rightValue {
				return -1
			}
			if leftValue > rightValue {
				return 1
			}
		}
	}
	if len(leftParts) < len(rightParts) {
		return -1
	}
	if len(leftParts) > len(rightParts) {
		return 1
	}
	return 0
}

type naturalPart struct {
	value  string
	number bool
}

func naturalParts(value string) []naturalPart {
	runes := []rune(value)
	parts := make([]naturalPart, 0)
	for start := 0; start < len(runes); {
		digit := runes[start] >= '0' && runes[start] <= '9'
		end := start + 1
		for end < len(runes) && (runes[end] >= '0' && runes[end] <= '9') == digit {
			end++
		}
		parts = append(parts, naturalPart{value: string(runes[start:end]), number: digit})
		start = end
	}
	return parts
}
