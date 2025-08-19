package hodu

import "runtime"
import "strings"
import "github.com/prometheus/client_golang/prometheus"

type ServerCollector struct {
	server           *Server
	BuildInfo        *prometheus.Desc
	ServerConns      *prometheus.Desc
	ServerRoutes     *prometheus.Desc
	ServerPeers      *prometheus.Desc
	SshProxySessions *prometheus.Desc
	PtySessions      *prometheus.Desc
	RptySessions     *prometheus.Desc
	RpxSessions      *prometheus.Desc
}

// NewServerCollector returns a new ServerCollector with all prometheus.Desc initialized
func NewServerCollector(server *Server) ServerCollector {
	var prefix string

	// prometheus doesn't like a dash. change it to an underscore
	prefix = strings.ReplaceAll(server.Name(), "-", "_") + "_"
	return ServerCollector{
		server: server,

		BuildInfo: prometheus.NewDesc(
			prefix + "build_info",
			"Build information",
			[]string{
				"goarch",
				"goos",
				"goversion",
			}, nil,
		),

		ServerConns: prometheus.NewDesc(
			prefix + "server_conns",
			"Number of server connections from clients",
			nil, nil,
		),
		ServerRoutes: prometheus.NewDesc(
			prefix + "server_routes",
			"Number of server-side routes",
			nil, nil,
		),
		ServerPeers: prometheus.NewDesc(
			prefix + "server_peers",
			"Number of server-side peers",
			nil, nil,
		),
		SshProxySessions: prometheus.NewDesc(
			prefix + "pxy_ssh_sessions",
			"Number of SSH proxy sessions",
			nil, nil,
		),
		PtySessions: prometheus.NewDesc(
			prefix + "pty_sessions",
			"Number of pty session",
			nil, nil,
		),
		RptySessions: prometheus.NewDesc(
			prefix + "rpty_sessions",
			"Number of rpty session",
			nil, nil,
		),
		RpxSessions: prometheus.NewDesc(
			prefix + "rpx_sessions",
			"Number of rpx session",
			nil, nil,
		),
	}
}

func (c ServerCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.BuildInfo
	ch <- c.ServerConns
	ch <- c.ServerRoutes
	ch <- c.ServerPeers
	ch <- c.SshProxySessions
	ch <- c.PtySessions
	ch <- c.RptySessions
	ch <- c.RpxSessions
}

func (c ServerCollector) Collect(ch chan<- prometheus.Metric) {

	ch <- prometheus.MustNewConstMetric(
		c.BuildInfo,
		prometheus.GaugeValue,
		1,
		runtime.GOARCH,
		runtime.GOOS,
		runtime.Version(),
	)

	ch <- prometheus.MustNewConstMetric(
		c.ServerConns,
		prometheus.GaugeValue,
		float64(c.server.stats.conns.Load()),
	)

	ch <- prometheus.MustNewConstMetric(
		c.ServerRoutes,
		prometheus.GaugeValue,
		float64(c.server.stats.routes.Load()),
	)

	ch <- prometheus.MustNewConstMetric(
		c.ServerPeers,
		prometheus.GaugeValue,
		float64(c.server.stats.peers.Load()),
	)

	ch <- prometheus.MustNewConstMetric(
		c.SshProxySessions,
		prometheus.GaugeValue,
		float64(c.server.stats.ssh_proxy_sessions.Load()),
	)

	ch <- prometheus.MustNewConstMetric(
		c.PtySessions,
		prometheus.GaugeValue,
		float64(c.server.stats.pty_sessions.Load()),
	)

	ch <- prometheus.MustNewConstMetric(
		c.RptySessions,
		prometheus.GaugeValue,
		float64(c.server.stats.rpty_sessions.Load()),
	)

	ch <- prometheus.MustNewConstMetric(
		c.RpxSessions,
		prometheus.GaugeValue,
		float64(c.server.stats.rpx_sessions.Load()),
	)
}
