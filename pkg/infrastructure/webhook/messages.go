package webhook

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/nonchan7720/kubeglance/pkg/domain"
)

// messages writes the title and body of a notification, short enough for a
// phone's lock screen: the title says what and where, the body the numbers.
// Numbers are formatted like the UI's.
type messages struct {
	lang   string // "en" or "ja"
	window time.Duration
}

func (m messages) ja() bool { return m.lang == "ja" }

func (m messages) text(a domain.Alert) (title, body string) {
	c := a.Verdict.Condition
	st := a.Verdict.Stats
	pick := func(r domain.Resource) int64 {
		if c.Resource == domain.ResourceCPU {
			return r.CPU
		}
		return r.Memory
	}
	pct := func(v int64) string { return percent(v, c.Total) }
	who, where := m.target(a.Key)
	res, total := m.names(a.Key)
	fired := m.duration(a.At.Sub(a.Since))
	thr := strconv.Itoa(int(math.Round(c.Threshold*100))) + "%"

	switch {
	case a.Status == domain.AlertFiring:
		size := m.amount(c.Resource, c.Total)
		if m.ja() {
			title = fmt.Sprintf("%s: %sが %s の %s", who, res, total, pct(pick(st.Median)))
			body = fmt.Sprintf("%s\n%s間: 中央値 %s · p95 %s · 現在 %s（%s 中）", where, m.duration(m.window), pct(pick(st.Median)), pct(pick(st.P95)), pct(pick(st.Now)), size)
		} else {
			title = fmt.Sprintf("%s: %s at %s of %s", who, res, pct(pick(st.Median)), total)
			body = fmt.Sprintf("%s\nover %s: median %s · p95 %s · now %s of %s", where, m.duration(m.window), pct(pick(st.Median)), pct(pick(st.P95)), pct(pick(st.Now)), size)
		}
		if st.Coverage < 1 && st.Expected > 0 {
			missing := strconv.Itoa(int(math.Round((1-st.Coverage)*100))) + "%"
			if m.ja() {
				body += "\nウィンドウの " + missing + " はデータなし"
			} else {
				body += "\n" + missing + " of the window has no data"
			}
		}
	case a.Gone:
		kind := map[bool]string{true: "pod", false: "node"}[a.Key.Node == ""]
		if m.ja() {
			title = fmt.Sprintf("%s: 解消（%s が消滅）", who, map[string]string{"pod": "Pod", "node": "ノード"}[kind])
			body = fmt.Sprintf("%s\n%s間 発火", where, fired)
		} else {
			title = fmt.Sprintf("%s: resolved, the %s is gone", who, kind)
			body = fmt.Sprintf("%s\nfired for %s", where, fired)
		}
	default:
		if m.ja() {
			title = fmt.Sprintf("%s: %sが %s の %s 未満に回復", who, res, total, thr)
			body = fmt.Sprintf("%s\n現在 %s · %s間 発火", where, pct(pick(st.Now)), fired)
		} else {
			title = fmt.Sprintf("%s: %s back under %s of %s", who, res, thr, total)
			body = fmt.Sprintf("%s\nnow %s · fired for %s", where, pct(pick(st.Now)), fired)
		}
	}
	return title, body
}

// target is a short name for the title and the full one for the body.
func (m messages) target(k domain.AlertKey) (short, full string) {
	if k.Node != "" {
		if m.ja() {
			return "ノード " + k.Node, "ノード " + k.Node
		}
		return "node " + k.Node, "node " + k.Node
	}
	return k.Pod, k.Namespace + "/" + k.Pod + " · " + k.Container
}

// names is the resource and what it is measured against. The English
// resource is lower case except CPU, as it sits mid-sentence.
func (m messages) names(k domain.AlertKey) (resource, total string) {
	total = "limit"
	if k.Node != "" {
		total = "allocatable"
	}
	switch {
	case strings.HasSuffix(k.Rule, "-cpu"):
		return "CPU", total
	case m.ja():
		return "メモリ", total
	default:
		return "memory", total
	}
}

func (m messages) summaryTitle(n int) string {
	if m.ja() {
		return fmt.Sprintf("kubeglance: アラート %d 件", n)
	}
	return fmt.Sprintf("kubeglance: %d alerts", n)
}

func (m messages) more(n int) string {
	if m.ja() {
		return fmt.Sprintf("\nほか %d 件", n)
	}
	return fmt.Sprintf("\n…and %d more", n)
}

// duration in whole minutes: "24m", "1h30m"; "24分", "1時間30分".
func (m messages) duration(d time.Duration) string {
	mins := max(1, int(d.Round(time.Minute)/time.Minute))
	h, mm := mins/60, mins%60
	switch {
	case m.ja() && h == 0:
		return fmt.Sprintf("%d分", mm)
	case m.ja():
		return fmt.Sprintf("%d時間%d分", h, mm)
	case h == 0:
		return fmt.Sprintf("%dm", mm)
	default:
		return fmt.Sprintf("%dh%dm", h, mm)
	}
}

// amount formats a total like the UI: millicores or cores, bytes in
// binary units.
func (m messages) amount(resource string, v int64) string {
	if resource == domain.ResourceCPU {
		switch {
		case v >= 10000:
			return strconv.FormatInt((v+500)/1000, 10) + " cores"
		case v >= 1000:
			return strconv.FormatFloat(float64(v)/1000, 'f', 2, 64) + " cores"
		default:
			return strconv.FormatInt(v, 10) + "m"
		}
	}
	units := []string{"B", "Ki", "Mi", "Gi", "Ti"}
	f := float64(v)
	i := 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	switch {
	case i == 0:
		return strconv.FormatInt(v, 10) + "B"
	case f >= 100:
		return strconv.FormatFloat(f, 'f', 0, 64) + units[i]
	default:
		return strconv.FormatFloat(f, 'f', 1, 64) + units[i]
	}
}

// percent is v of total, rounded to a whole percent.
func percent(v, total int64) string {
	if total <= 0 {
		return "—"
	}
	return strconv.FormatInt(int64(math.Round(float64(v)*100/float64(total))), 10) + "%"
}
