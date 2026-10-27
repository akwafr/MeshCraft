package edgeswitch

import (
	"context"

	"meshcraft/internal/driver"
)

// driver.LLDPReader adapter — the CLI neighbour read, in the shape the
// read-state gesture consumes.
//
// This platform's SNMP agent DOES serve the standard LLDP-MIB (measured:
// the lab unit's `lldp_self` row carries its chassis and every port), so the
// proxy's periodic walk remains its primary path. The CLI adapter exists
// anyway, and deliberately: the same table read on the session read-state
// already holds costs one `show`, refreshes the observation at the moment an
// operator is actually looking, and keeps this connector exercising the same
// contract as the platforms that NEED it (fs/s3400). Both paths write the
// same rows — same local port names, same canonical chassis spelling once
// the API layer normalizes — so whichever wrote last told the same truth.

// lldpObservationFrom translates the parsed `show lldp remote-device all`
// table. PURE, tested against the captured lab table (testdata).
//
// Enabled is TRUE by construction here: this summary carries no "LLDP is
// disabled" marker — a port with the protocol off simply shows an empty row,
// like a port with no neighbour. The command having answered is the only
// evidence this platform offers, and claiming more would be inventing.
func lldpObservationFrom(rows []LLDPNeighbor) driver.LLDPObservation {
	var obs = driver.LLDPObservation{Enabled: true}
	var i int
	for i = range rows {
		obs.Neighbors = append(obs.Neighbors, driver.LLDPNeighborObservation{
			LocalPort: rows[i].LocalPort,
			ChassisID: rows[i].ChassisID,
			PortID:    rows[i].PortID,
			SysName:   rows[i].SysName,
		})
	}
	return obs
}

// ReadLLDPObservation implements driver.LLDPReader.
func (c *Client) ReadLLDPObservation(ctx context.Context) (driver.LLDPObservation, error) {
	var rows, err = c.ReadLLDPNeighbors(ctx)
	if err != nil {
		return driver.LLDPObservation{}, err
	}
	return lldpObservationFrom(rows), nil
}
