package hodu

import "runtime"
import "strings"
import "github.com/prometheus/client_golang/prometheus"

type ClientCollector struct {
	client           *Client
	BuildInfo        *prometheus.Desc
	ClientConns      *prometheus.Desc
	ClientRoutes     *prometheus.Desc
	ClientPeers      *prometheus.Desc
	PtsSessions      *prometheus.Desc
}

// NewClientCollector returns a new ClientCollector with all prometheus.Desc initialized
func NewClientCollector(client *Client) ClientCollector {
	var prefix string

	// prometheus doesn't like a dash. change it to an underscore
	prefix = strings.ReplaceAll(client.Name(), "-", "_") + "_"
	return ClientCollector{
		client: client,

		BuildInfo: prometheus.NewDesc(
			prefix + "build_info",
			"Build information",
			[]string{
				"goarch",
				"goos",
				"goversion",
			}, nil,
		),

		ClientConns: prometheus.NewDesc(
			prefix + "client_conns",
			"Number of client connections from clients",
			nil, nil,
		),
		ClientRoutes: prometheus.NewDesc(
			prefix + "client_routes",
			"Number of client-side routes",
			nil, nil,
		),
		ClientPeers: prometheus.NewDesc(
			prefix + "client_peers",
			"Number of client-side peers",
			nil, nil,
		),
		PtsSessions: prometheus.NewDesc(
			prefix + "pts_sessions",
			"Number of pts sessions",
			nil, nil,
		),
	}
}

func (c ClientCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.BuildInfo
	ch <- c.ClientConns
	ch <- c.ClientRoutes
	ch <- c.ClientPeers
	ch <- c.PtsSessions
}

func (c ClientCollector) Collect(ch chan<- prometheus.Metric) {

	ch <- prometheus.MustNewConstMetric(
		c.BuildInfo,
		prometheus.GaugeValue,
		1,
		runtime.GOARCH,
		runtime.GOOS,
		runtime.Version(),
	)

	ch <- prometheus.MustNewConstMetric(
		c.ClientConns,
		prometheus.GaugeValue,
		float64(c.client.stats.conns.Load()),
	)

	ch <- prometheus.MustNewConstMetric(
		c.ClientRoutes,
		prometheus.GaugeValue,
		float64(c.client.stats.routes.Load()),
	)

	ch <- prometheus.MustNewConstMetric(
		c.ClientPeers,
		prometheus.GaugeValue,
		float64(c.client.stats.peers.Load()),
	)

	ch <- prometheus.MustNewConstMetric(
		c.PtsSessions,
		prometheus.GaugeValue,
		float64(c.client.stats.pts_sessions.Load()),
	)
}
