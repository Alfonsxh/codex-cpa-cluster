package notifications

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/quota"
)

func UsageCenterURL(publicBaseURL string) string {
	value := strings.TrimSpace(publicBaseURL)
	if value == "" {
		return ""
	}
	return strings.TrimRight(value, "/") + "/usage/"
}

func QuotaRows(snapshot Snapshot, thresholdPercent float64, onlyKeys map[string]struct{}) []Row {
	rows := make([]Row, 0)
	for _, account := range snapshot.Accounts {
		windows := convertWindows(account.Quota)
		hadWindows := len(windows) > 0
		filtered := windows[:0]
		for _, window := range windows {
			if !strings.Contains(strings.ToLower(window.Label), "gpt-5.3") {
				filtered = append(filtered, window)
			}
		}
		windows = filtered
		if len(windows) == 0 {
			if hadWindows {
				continue
			}
			key := account.ID + "|unavailable"
			if len(onlyKeys) > 0 {
				if _, found := onlyKeys[key]; !found {
					continue
				}
			}
			rows = append(rows, Row{
				Key: key, Account: defaultString(account.ID, "unknown"), Label: "常规周限额",
				ActiveUsers: account.ActiveUsers1H, ResetCount: account.Quota.ResetCreditCount,
				Level: "unavailable",
			})
			continue
		}
		for _, window := range windows {
			key := defaultString(account.ID, "unknown") + "|" + defaultString(window.Key, "default:primary_window")
			if len(onlyKeys) > 0 {
				if _, found := onlyKeys[key]; !found {
					continue
				}
			}
			used := math.Max(0, math.Min(window.UsedPercent, 100))
			level := "normal"
			switch {
			case account.Quota.Status != "ok" || math.IsNaN(used) || math.IsInf(used, 0):
				level = "unavailable"
			case window.LimitReached || used >= 100:
				level = "exhausted"
			case used >= thresholdPercent:
				level = "warning"
			}
			usedCopy := used
			if level == "unavailable" {
				usedCopy = 0
			}
			row := Row{
				Key: key, Account: defaultString(account.ID, "unknown"), Label: defaultString(window.Label, "常规周限额"),
				UsedPercent: &usedCopy, ActiveUsers: account.ActiveUsers1H,
				ResetCount: account.Quota.ResetCreditCount, ResetAt: window.ResetAt,
				ResetKey: window.ResetAt, Level: level,
			}
			if level == "unavailable" {
				row.UsedPercent = nil
			}
			rows = append(rows, row)
		}
	}
	sort.SliceStable(rows, func(left int, right int) bool {
		leftUnavailable := rows[left].Level == "unavailable" || rows[left].UsedPercent == nil
		rightUnavailable := rows[right].Level == "unavailable" || rows[right].UsedPercent == nil
		if leftUnavailable != rightUnavailable {
			return !leftUnavailable
		}
		if !leftUnavailable && *rows[left].UsedPercent != *rows[right].UsedPercent {
			return *rows[left].UsedPercent < *rows[right].UsedPercent
		}
		if compared := naturalCompare(rows[left].Account, rows[right].Account); compared != 0 {
			return compared < 0
		}
		return naturalCompare(rows[left].Label, rows[right].Label) < 0
	})
	return rows
}

type quotaWindow struct {
	Key          string
	Label        string
	UsedPercent  float64
	ResetAt      *int64
	LimitReached bool
}

func convertWindows(accountQuota quota.AccountQuota) []quotaWindow {
	sources := accountQuota.WeeklyWindows
	if len(sources) == 0 && accountQuota.Weekly != nil {
		sources = []quota.WeeklyWindow{*accountQuota.Weekly}
	}
	result := make([]quotaWindow, 0, len(sources))
	for _, window := range sources {
		key := window.Key
		if key == "" {
			key = "default:primary_window"
		}
		result = append(result, quotaWindow{
			Key: key, Label: window.Label, UsedPercent: window.UsedPercent,
			ResetAt: window.ResetAt, LimitReached: window.LimitReached,
		})
	}
	return result
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
) (string, error) {
	if location == nil {
		location = time.UTC
	}
	rows := QuotaRows(snapshot, thresholdPercent, onlyKeys)
	transitions := make(map[string]string, len(transitionEvents))
	for key, value := range transitionEvents {
		transitions[key] = value
	}
	transitionLabels := map[string]string{
		"warning": "🟠 达到预警", "exhausted": "🔴 额度耗尽",
		"recovered": "🟢 额度恢复", "recovered_warning": "🟠 恢复至预警",
		"refreshed": "🔄 额度刷新",
	}
	icons := map[string]string{"normal": "🟢", "warning": "🟠", "exhausted": "🔴", "unavailable": "⚪"}
	table := []string{
		"| CPA账号 / 额度窗口 | 已用 | 1h用户 | 重置次数 | 下次刷新 |",
		"| :--- | ---: | ---: | ---: | :--- |",
	}
	if len(transitions) > 0 {
		table = []string{
			"| 事件 | CPA账号 / 额度窗口 | 已用 | 1h用户 | 重置次数 | 下次刷新 |",
			"| :--- | :--- | ---: | ---: | ---: | :--- |",
		}
	}
	for _, row := range rows {
		name := fmt.Sprintf("%s %s · %s", icons[row.Level], safeCell(row.Account, 32), safeCell(row.Label, 24))
		cells := []string{
			name, formatPercent(row.UsedPercent), strconv.Itoa(row.ActiveUsers),
			formatOptionalInt(row.ResetCount), formatReset(row.ResetAt, location, now),
		}
		if len(transitions) > 0 {
			label := transitionLabels[transitions[row.Key]]
			if label == "" {
				label = "—"
			}
			cells = append([]string{label}, cells...)
		}
		table = append(table, "| "+strings.Join(cells, " | ")+" |")
	}
	if len(rows) == 0 {
		if len(transitions) > 0 {
			table = append(table, "| — | ⚪ 暂无匹配账号 | — | 0 | — | — |")
		} else {
			table = append(table, "| ⚪ 暂无匹配账号 | — | 0 | — | — |")
		}
	}
	legend := "> 🟢 正常　🟠 超过阈值　🔴 额度耗尽　⚪ 数据不可用"
	if len(transitions) > 0 {
		legend += "　🔄 额度刷新"
	}
	sections := []string{
		"# " + safeCell(title, 64),
		fmt.Sprintf("> 统计时间：%s　预警阈值：%s", now.In(location).Format("2006-01-02 15:04"), formatPercentValue(thresholdPercent)),
	}
	if usageCenterURL = strings.TrimSpace(usageCenterURL); usageCenterURL != "" {
		sections = append(sections, fmt.Sprintf("> 应用地址：[%s](%s)", usageCenterURL, usageCenterURL))
	}
	sections = append(sections, strings.Join(table, "\n"), legend)
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
		return "等待刷新"
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
