package node

import "heckel.io/hostit/node/api"

// Screenshot renders one dashboard preview of the app on this machine and
// returns the PNG bytes. It is thin: the screenshot Engine owns the container,
// the per-shot egress firewall and the DevTools capture; control owns the
// scheduling, rate limiting, storage and serving around it.
func (m *Machine) Screenshot(spec *api.ScreenshotSpec) ([]byte, error) {
	return m.shots.Shoot(spec)
}
