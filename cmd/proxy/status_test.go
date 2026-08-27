package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"

	"heckel.io/hostit/proxy"
	"heckel.io/hostit/proxy/api"
)

func TestRenderProxyStatus(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	renderProxyStatus(&buf, &proxy.Status{
		ProxyID: "edge-1", Version: "1.2.3", ControlURL: "http://127.0.0.1:2586", ClusterURL: "10.0.0.1:2930",
		Connected: true, TableSeq: 47, Routes: 12, CertsCached: 8,
	})
	out := buf.String()
	for _, want := range []string{"edge-1", "1.2.3", "10.0.0.1:2930", "connected", "47", "12", "8"} {
		assert.Contains(t, out, want)
	}

	buf.Reset()
	renderProxyStatus(&buf, &proxy.Status{ProxyID: "edge-1"})
	assert.Contains(t, buf.String(), "NOT CONNECTED")
}

func TestRenderRoutes(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	renderRoutes(&buf, &api.Table{Seq: 47, Routes: []api.Route{
		{Host: "blog.example.com", Target: "10.0.0.2:10001"},
	}})
	out := buf.String()
	for _, want := range []string{"HOST", "TARGET", "blog.example.com", "10.0.0.2:10001", "47"} {
		assert.Contains(t, out, want)
	}

	buf.Reset()
	renderRoutes(&buf, &api.Table{})
	assert.Contains(t, buf.String(), "no routes", "an empty table says so instead of drawing nothing")
}
