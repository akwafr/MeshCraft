package routeros

// Identity on the ALREADY-OPEN access (driver.IdentityReader), the re-read twin
// of [Driver.Identify]. In its own file so inspect.go keeps the discovery-path
// pair ([Inspect] / [Driver.Identify]) together.

import (
	"context"

	"meshcraft/internal/driver"
)

var _ driver.IdentityReader = (*Client)(nil)

// ReadIdentity reads the same objects as discovery identification —
// `system/resource`, `system/routerboard`, `system/identity`, the interface
// menus — but on the client the caller already holds, exactly the case
// [Client.Identify]'s comment promises: "a caller with other reads to make
// does not open a second client". A few REST GETs on a proven session, no new
// authentication. Partial on error, like everything on this platform: a CHR
// without `/system/routerboard` still returns model and firmware.
func (c *Client) ReadIdentity(ctx context.Context) (driver.Identity, error) {
	var dev, err = c.Identify(ctx)
	return dev.Standard(), err
}
