//go:build windows

package libbox

import (
	"github.com/sagernet/sing-box/adapter/inbound"
	"github.com/sagernet/sing-box/protocol/mixed"
)

func registerPlatformInbounds(registry *inbound.Registry) {
	mixed.RegisterInbound(registry)
}
