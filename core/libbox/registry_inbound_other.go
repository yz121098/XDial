//go:build !windows

package libbox

import "github.com/sagernet/sing-box/adapter/inbound"

func registerPlatformInbounds(_ *inbound.Registry) {}
