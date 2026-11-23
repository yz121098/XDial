package trayicon

import (
	"bytes"
	"image/png"
	"testing"
)

func TestRenderAllSixTrayStates(t *testing.T) {
	cases := []struct {
		name string
		spec Spec
	}{
		{"disconnected_light", Spec{State: StateDisconnected, Badge: BadgeNone, DarkTaskbar: false}},
		{"disconnected_dark", Spec{State: StateDisconnected, Badge: BadgeNone, DarkTaskbar: true}},
		{"connecting_pulse_0", Spec{State: StateConnecting, Badge: BadgeNone, PulseFrame: 0, DarkTaskbar: true}},
		{"connecting_pulse_5", Spec{State: StateConnecting, Badge: BadgeNone, PulseFrame: 5, DarkTaskbar: true}},
		{"connected", Spec{State: StateConnected, Badge: BadgeNone, DarkTaskbar: true}},
		{"proxy_only_bite", Spec{State: StateProxyOnly, Badge: BadgeNone, DarkTaskbar: true}},
		{"update_dot_overlay", Spec{State: StateConnected, Badge: BadgeUpdate, DarkTaskbar: true}},
		{"error_overrides_update", Spec{State: StateConnected, Badge: BadgeError, DarkTaskbar: true}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rawPNG := RenderPNG(tc.spec)
			if len(rawPNG) == 0 {
				t.Fatalf("empty PNG output for %s", tc.name)
			}
			img, err := png.Decode(bytes.NewReader(rawPNG))
			if err != nil {
				t.Fatalf("decode PNG %s: %v", tc.name, err)
			}
			if img.Bounds().Dx() != 64 || img.Bounds().Dy() != 64 {
				t.Fatalf("unexpected dimensions: %v", img.Bounds())
			}

			ico := RenderICO(tc.spec)
			if len(ico) <= 22 {
				t.Fatalf("invalid ICO output length: %d", len(ico))
			}
		})
	}
}
