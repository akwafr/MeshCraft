---
tags: [network, orchestration, data]
---

# Default VLAN

Two rules, a single cause: **VLAN 1 is not an operator's decision,
it is a hardware fact**. Every switch leaves the factory with it, and none allows
getting rid of it. meshcraft therefore cannot treat it as an ordinary VLAN without
describing a fleet that does not exist.

## 1. Every site has VLAN 1, and cannot lose it

`store.CreateSite` inserts the site **and** its VLAN 1 in the same transaction. No
two-step creation: there must be no instant at which a site exists
without its default VLAN, otherwise the first device read-back reports an
"unknown" VLAN that is anything but unknown.

Migration `0018_default_vlan.sql` catches up with existing data. It is written with
`where not exists`: a site that already described its VLAN 1 keeps **its** name and
**its** colour — the rule imposes presence, not labelling.

On the API side, `handleDeleteVlan` and `handleUpdateVlan` refuse with a `409`:

| Action on VLAN 1 | Result |
|---|---|
| Delete | refused — erasing it from the site would not remove it from anywhere |
| Change its `vlan_id` | refused — it would be deleting it by another path |
| Rename, recolour, describe | **allowed** — that is presentation |

The *Configuration → VLANs* screen replaces the delete button with the label
"default" and locks the ID field. The safeguard nevertheless stays on the
server side: the interface is only a courtesy, the API is the rule.

> [!warning] One door remains open: site configuration import
> `store.ImportSiteConfig` **purges** the site's `vlans` table then reinserts those
> from the file ([[Standard data model]], § *Exchange format*). A
> `meshcraft_site_config` file written by hand without VLAN 1 therefore leaves the site without its
> default VLAN — the rule is held at **creation** and by the **migration**,
> not at import.
>
> The case cannot be reached through a meshcraft round trip: export reads the database, where
> VLAN 1 is always there. It is reached through a fabricated file, which is
> precisely the use this format exists to allow. The repair is that
> of migration `0018`, in the same place: a `where not exists` after the
> reinsertion. Three lines, against a site whose first device read-back
> would report an "unknown" VLAN that is anything but unknown.

## 2. A trunk without an explicit native VLAN has VLAN 1 as native

> [!important] Unless explicitly native
> A requested PVID always wins. VLAN 1 is only the native when **no**
> other is designated — and the port is a **trunk**: on the server side
> `recon.ParsePorts` only substitutes the default on `mode == "trunk"`, on the screen side
> `nativeVlan()` returns `null` for an access port without a PVID.

This is not a convenience convention, it is the correction of a **false
drift**. The device always reports a native VLAN; if it received nothing, it
reports 1. An intent stored as `pvid: 0` would therefore never match
the observation, and [[Reconciliation]] would display `port.GEx.pvid: 0 → 1` on every
trunk in the fleet, permanently, without anyone having touched
anything. Worse: apply would try to "correct" the gap by removing the
native VLAN on every pass.

The normalisation lives in `recon.ParsePorts`, that is, **at the intent's read
boundary**. Everything that consumes a port configuration —
drift comparison, automatic apply, translation into per-VLAN
membership — goes through there and inherits the rule without restating it. A `libre` (free) port
gains nothing from it: the absence of intent is not an intent.

Rendering follows the same logic via `nativeVlan()` in `web/src/network.ts`: a
trunk without a designated native is coloured with VLAN 1 and answers "yes" when asked
whether it carries VLAN 1.

## 3. VLAN 1 is not a table row — and what that changes for writing

The rule "every switch has VLAN 1" is borne out on hardware, but **not in the
form one would expect**: on the two shipped MikroTik platforms, VLAN 1
exists *without* the VLAN table declaring it, and that is a difference of nature,
not of presentation.

| Platform | How VLAN 1 exists | Observation |
|---|---|---|
| **SwOS** (CSS326) | **implicitly**. The two SFP cages of the reference device have `dvid = 1` whereas its `/vlan.b` table contains ONLY VLANs 99 and 100 | `dvid[24] = dvid[25] = 0x01`, two-row table |
| **RouterOS** (CRS326) | through a **DYNAMIC** entry the device makes for itself | `{"vlan-ids":"1","dynamic":"true","comment":"added by pvid","untagged":"bridge,sfp-sfpplus2"}` |

Three consequences, all in the write code:

1. **Never create the VLAN 1 row on SwOS.** Creating it would turn
   VLAN 1 into a *declared* VLAN, of which only the listed ports would be members —
   that is, it would change the behaviour of all the device's untagged
   traffic, although nobody asked for it. `swos.implicitVLAN` carries this
   rule, and the first version of the connector failed the other way: it looked for
   a row for VLAN 1 and complained about its absence on a perfectly
   configured device.
2. **Never remove anything from VLAN 1 on SwOS.** It has no row: there is
   nothing to remove from it, and mentioning it in a refusal would have people looking for an impossible
   removal on every port of a factory-fresh device.
3. **Never write into a dynamic entry on RouterOS.** It has no stable
   `.id`, it cannot be modified, and it disappears when the reason that
   created it disappears. The write creates a **static entry alongside**, and says so in the
   transcript because it is counter-intuitive: we "add" a VLAN that
   already appeared in the read.

> [!important] The implicit native VLAN of §2 is what makes `admit-only-vlan-tagged` unwritable
> The rule of §2 — a trunk without an explicit native has VLAN 1 as native — has a
> consequence nobody had seen coming on the RouterOS side: **meshcraft cannot
> express a trunk that refuses untagged traffic**.
>
> A meshcraft trunk *always* has a native VLAN, since `recon.ParsePorts` puts
> VLAN 1 there when the intent designates none. Yet a native VLAN means "this traffic
> enters and leaves this port without a tag". Writing
> `frame-types=admit-only-vlan-tagged` — the value a "clean" trunk should
> carry on a CRS3xx — would therefore drop precisely the native VLAN traffic the
> plan asks for.
>
> The connector leaves trunks at `admit-all`, what RouterOS calls a
> **hybrid** port, and that is exactly what the model describes. The limit is real and
> it is *upstream* of the connectors: it is the standard model that lacks
> the "trunk without native" intent. Lifting it would require a `pvid: null` distinct from
> `pvid: 0` in `devices.ports`, and hence reopening the false drift that §2
> exists to avoid. It is not a trade-off to be made lightly.

## See also

- [[Reconciliation]] — why an intent must be comparable to a measurement
- [[Standard data model]] — the stored form of `devices.ports`
- [[Management addressing]] — the other VLAN we do not treat like the others
- [[Connectors]] — §*The VLAN model of the two MikroTik platforms*: what each
  platform carries, what the write puts there, and what it refuses
- [[Switch profiles]] — what a rule can ask for, and what no connector
  will ever be able to set
