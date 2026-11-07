package handler

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// metrics serves kubeglance's own metrics in the Prometheus text exposition
// format: is it collecting, how long a poll takes, how much history it holds.
// Written by hand to keep the binary free of a metrics library; it is a
// dozen gauges and counters.
func (h *handler) metrics(w http.ResponseWriter, _ *http.Request) {
	health := h.Collector.Health()
	size := h.Store.Size()
	var b strings.Builder
	metric := func(name, typ, help string, value float64, labels ...string) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n%s", name, help, name, typ, name)
		if len(labels) > 0 {
			b.WriteString("{")
			for i := 0; i+1 < len(labels); i += 2 {
				if i > 0 {
					b.WriteString(",")
				}
				fmt.Fprintf(&b, "%s=%s", labels[i], strconv.Quote(labels[i+1]))
			}
			b.WriteString("}")
		}
		b.WriteString(" " + strconv.FormatFloat(value, 'g', -1, 64) + "\n")
	}
	bit := func(v bool) float64 {
		if v {
			return 1
		}
		return 0
	}
	lastSuccess := 0.0
	if !health.LastSuccess.IsZero() {
		lastSuccess = float64(health.LastSuccess.UnixMilli()) / 1000
	}

	metric("kubeglance_build_info", "gauge", "Always 1; the version is a label.", 1, "version", h.Version)
	metric("kubeglance_ready", "gauge", "1 once the history is copied and the first round is recorded (what /readyz says).", bit(health.Rounds > 0))
	metric("kubeglance_collector_healthy", "gauge", "1 while a poll succeeded within the last two intervals.", bit(health.Healthy))
	metric("kubeglance_poll_rounds_total", "counter", "Successful polls of metrics-server since start.", float64(health.Rounds))
	metric("kubeglance_poll_errors_total", "counter", "Failed polls of metrics-server since start.", float64(health.Errors))
	metric("kubeglance_poll_last_duration_seconds", "gauge", "How long the last poll took.", health.LastDuration.Seconds())
	metric("kubeglance_poll_last_success_timestamp_seconds", "gauge", "Unix time of the last successful poll (0 before the first).", lastSuccess)
	metric("kubeglance_poll_interval_seconds", "gauge", "Current poll interval (metrics-server's resolution unless --interval fixes it).", h.Store.Interval().Seconds())
	metric("kubeglance_store_pods", "gauge", "Pods in history, removed ones included until they leave the retention.", float64(size.Pods))
	metric("kubeglance_store_nodes", "gauge", "Nodes in history.", float64(size.Nodes))
	metric("kubeglance_store_series", "gauge", "Series held (pods, nodes, containers of multi-container pods).", float64(size.Series))
	metric("kubeglance_store_samples", "gauge", "Samples held across all series.", float64(size.Samples))
	metric("kubeglance_store_series_capacity", "gauge", "Samples each series can hold (retention / finest interval + 1).", float64(size.Capacity))
	metric("kubeglance_store_retention_seconds", "gauge", "How far back history is kept.", h.Store.Retention().Seconds())

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if _, err := w.Write([]byte(b.String())); err != nil {
		h.Log.Debug("write metrics", "err", err)
	}
}
