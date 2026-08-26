package node

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"heckel.io/hostit/hoststats"
)

// Node metrics, exposed on the optional listen-metrics endpoint. The gauges are
// refreshed by MetricsLoop (one host measurement per interval, not per scrape);
// the counters are bumped inline on the machine's work.
var (
	nodeApps      = promauto.NewGauge(prometheus.GaugeOpts{Name: "hostit_node_apps", Help: "Apps hosted on this node."})
	nodeMemUsed   = promauto.NewGauge(prometheus.GaugeOpts{Name: "hostit_node_memory_used_bytes", Help: "Used host memory."})
	nodeMemTotal  = promauto.NewGauge(prometheus.GaugeOpts{Name: "hostit_node_memory_total_bytes", Help: "Total host memory."})
	nodeDiskUsed  = promauto.NewGauge(prometheus.GaugeOpts{Name: "hostit_node_disk_used_bytes", Help: "Used apps-pool disk."})
	nodeDiskTotal = promauto.NewGauge(prometheus.GaugeOpts{Name: "hostit_node_disk_total_bytes", Help: "Total apps-pool disk."})
	nodeLoad1     = promauto.NewGauge(prometheus.GaugeOpts{Name: "hostit_node_load1", Help: "Host 1-minute load average."})
	nodeExecs     = promauto.NewCounter(prometheus.CounterOpts{Name: "hostit_node_execs_total", Help: "Commands run in app containers on this node."})
)

// metricsInterval is how often the node re-measures the host for its gauges.
const metricsInterval = 15 * time.Second

// MetricsLoop refreshes the node's resource/app gauges until done closes.
func (m *Machine) MetricsLoop(done <-chan struct{}) {
	update := func() {
		st := hoststats.Measure(m.config.AppsDir)
		nodeMemUsed.Set(float64(st.MemoryUsedMB) * 1e6)
		nodeMemTotal.Set(float64(st.MemoryTotalMB) * 1e6)
		nodeDiskUsed.Set(float64(st.DiskUsedMB) * 1e6)
		nodeDiskTotal.Set(float64(st.DiskTotalMB) * 1e6)
		nodeLoad1.Set(st.Load1)
		if apps, err := m.store.Apps(); err == nil {
			nodeApps.Set(float64(len(apps)))
		}
	}
	update()
	for {
		select {
		case <-done:
			return
		case <-time.After(metricsInterval):
			update()
		}
	}
}
