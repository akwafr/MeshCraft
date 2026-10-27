---
tags: [frontend, model]
---

# Profiles and groups

The "UniFi-style" mechanism: define a configuration **once**, apply it to sets of ports and devices, and manage only the exceptions.

## Where this page stands

The mechanism exists, but **not in the form of inheritance**: it is a **set of rules** that produces a configuration, and the operator reviews it before writing it. The difference is developed in [[Switch profiles]]; it changes what this page can promise.

| Question | Status |
|---|---|
| Site **VLAN**: id, name, colour, usage | **shipped** — plus a structural, undeletable VLAN 1 (migration `0018`, [[Default VLAN]]) |
| **Port profile**: mode, PVID, tagged, PoE | **shipped** — *Configuration → Port profiles* screen, applied from the switch's page |
| **Switch profile**: rules → faceplate configuration, with **preview** | **shipped** — `internal/portrules`, [[Switch profiles]] |
| Preview distinguishing *designated* and *obtained*, uncovered ports, wholesale refusal | **shipped** — and it is the most accomplished safeguard on the page |
| A port's incompatibility with what a rule asks, reported **before** writing | **shipped for switch profiles**; for direct application of a port profile, PoE is silently corrected |
| **Device profile**: common services (STP, SNMP, syslog, NTP…) and default assignments | **entry screen shipped, nothing compiles it** |
| **Device group** | **legacy** — no screen any more, model kept for credential scope |
| **Resolution** chain by inheritance (site defaults → profile → overrides) | **decided, not built** |
| "Inherited from: profile *office*" / "local override" badge, "revert to inherited" button | **decided, not built** — an applied profile **copies** its values |
| Recompiling the devices concerned when a profile changes, "n devices to deploy" banner | **decided, not built** |
| Refusal to delete a **referenced** VLAN or profile | **decided, not built** — deletion succeeds (callout below) |
| Profiles **versioned** like the intent, history of who changed what | **decided, not built** |

## Objects

| Object | Scope | Content | Status |
|---|---|---|---|
| **VLAN** | site | 802.1Q id, name, colour ([[Port rendering]]), usage (mgmt/users/voice/iot…) | **shipped** |
| **Port profile** | site | access/trunk mode, PVID, tagged, PoE — the `ports[].*` subtree of the [[Standard data model]] | **shipped**; voice VLAN, STP edge/guards and filters (dhcp snooping trust, RA guard, storm control) remain **decided, not built** |
| **Device profile** | site | common services: STP mode/priority, SNMP, syslog, NTP, DNS, mgmt (ssh/http/telnet on-off), igmp snooping + **default assignments** of port profiles | **entry shipped, compilation not built** |
| **Device group** | site | *legacy* — named set of devices; a group references a device profile. See the box below | **legacy** |
| **Switch profile** | site | rules that produce a device's port configuration from its faceplate — [[Switch profiles]] | **shipped** |

> [!warning] The group no longer has a screen
> The device profile it carries is compiled nowhere, and no interface
> has ever assigned a device to a group. Its only living function — deciding
> which credential applies — is better fulfilled by a **tag**
> ([[Tags and credential scope]]), which is not unique per device.
> The model stays in place as long as credentials target a group; §1 of
> [[Switch profiles]] describes the exit path, in stages. The old URL
> `/config/groupes` redirects to switch profiles.
>
> What the group promised for configuration — "adding a switch to the group
> is enough to make it compliant" — is taken over by the **switch profile**, which
> achieves it without having to sort the fleet beforehand.

> [!warning] The device profile has a screen, and nothing behind it
> It can be created, modified and deleted (*Configuration → Device profiles*),
> and **none of its values ever reaches a device**: nothing compiles it,
> and no screen attaches a device to it. Today it is a form whose
> content never leaves the database.
>
> It is not removed, because the list of common services it carries is
> exactly what will need to be pushed (§ *services* of the
> [[Standard data model]]) and half of those services are already set
> through other, better-targeted paths: **SNMP** through the service identity
> ([[SNMP identity]]), the **trap destination** through the declared collector
> ([[Monitoring]]). The device profile is therefore waiting for what remains —
> STP, NTP, DNS, management switches — and not for a rewrite.
>
> What to avoid until then: promising a customer that entering an NTP
> server there configures their fleet. The screen should say so itself.

## Resolution

**Targeted** order of increasing priority:

```
site defaults  →  device profile (via group)  →  device overrides
      →  port profile (assigned by default or by hand)  →  port overrides
```

> [!important] There is no inheritance: an applied profile COPIES its values
> This is the most important gap on the page, because it changes the nature of the
> object. A port's model (`PortIntent`) carries **no reference** to the
> profile it comes from: applying the *office* profile to twelve ports writes mode,
> PVID, tagged and PoE into them, and the link is lost at that very instant.
>
> What follows from this, and must be known before relying on the page:
>
> - there is **no origin badge** ("inherited from: profile *office*"), no
>   visible distinction between an inherited value and a local override, and no
>   "revert to inherited" button. Nothing says where what you read comes from;
> - **modifying a profile changes nothing on the fleet.** No recompilation, no
>   "n devices have changes to deploy" banner, no global diff.
>   Ports already configured keep the old values, and the screen does not
>   report it;
> - **deleting a profile breaks nothing** — precisely because nothing
>   references it.
>
> This is not merely a missing step. Copying has a real advantage, and it is
> the one [[Switch profiles]] exploits: what is written to the device is
> **exactly what the operator reviewed in the preview**, without a change
> elsewhere being able to modify it after the fact. A live inheritance is more powerful and
> more surprising: it moves the configuration of a switch nobody
> touched.
>
> The target remains inheritance, because "manage only the exceptions" is the
> product's promise and it does not hold without it. But the order to follow is
> not to add a reference in `PortIntent`: it is to **make
> recompilation visible before making it automatic** — the banner and the global
> diff first, propagation afterwards. The reverse would move fleets without
> anyone seeing why.

- Each level only defines what it wants to impose (per-field merge, "absent = inherits"): **target**. What exists and is its seed: applying a selection **merges** the patch with each port's own intent, so setting a VLAN on twelve ports does not erase their labels ([[Web interface]]).
- What the **switch profile** does instead, answering the same need from the other end: a rule names a **set** ("the last four RJ45 ports"), never an absolute port number. It therefore applies to a 48-port as well as to an 8-port switch — "that is the whole difference between a profile and a copy-paste". And the computation is **pure**: a faceplate and a set of rules go in, a port-by-port assignment comes out, without database, without device, without clock — because a decision that cannot be replayed identically cannot be reviewed.

## Examples

- Port profile `office-standard`: access PVID 20, PoE on. *(Voice VLAN, STP edge and BPDU guard are in the targeted model, not in the shipped form.)*
- Port profile `uplink`: trunk native 10, tagged all.
- Switch profile `floor-standard`: RJ45 ports except the last four → `office-standard`, the last four RJ45 and the SFP cages → `uplink`. Applied to a 48-port as to an 8-port switch, without rewriting.
- Device profile `floor-standard`: RSTP prio 32768, snmp v3, syslog to proxy — **can be entered, not written** (see the callout above).
- Group `Building B`: 12 switches → `floor-standard`. **Legacy**: the equivalent gesture is to apply the switch profile to the twelve devices, or to set a tag for a credential's scope.

## Safeguards

- Deleting a referenced VLAN/profile: refused as long as ports use it (the UI lists the usages).

> [!warning] This protection does not exist: deletion succeeds
> Deleting a VLAN or a profile checks **no usage**. The only refusal is
> structural: **VLAN 1** cannot be deleted, because devices
> have it anyway and it is the implicit native VLAN of trunks — removing it from the
> site would not remove it from anywhere, it would only make what the ports
> carry unreadable ([[Default VLAN]]).
>
> For all others, deletion goes through. Ports that carried the VLAN
> keep its number in their intent, but the site can no longer name or
> colour it: the cell loses its colour and becomes neutral again ([[Port rendering]]), so
> **the configuration becomes invisible without changing**. The consequence is more
> pernicious than an error: nothing breaks, the device stays configured as
> before, and the screen stops showing it.
>
> Two mitigations: deletion emits a **warning** event in the
> [[Event log]] (`vlan.deleted`, `profile.deleted`) with the author, and
> it requires `site_admin`. What is missing is the usage count — which the code
> already knows how to do, since the VLAN legend shows "n ports" per VLAN. It is this
> count that must be set against deletion.

- Profiles are versioned like the intent ([[Standard data model]]): history shows who changed what. **Decided, not built** — a profile is a row updated in place, without revision. What exists: the modification event in the [[Event log]], with author and date. That is *who and when*, not *what*: one cannot go back to the previous state.
- A profile incompatible with a device (missing capability, e.g. no PoE) is reported **at assignment**, not at deployment ([[Connectors]]).

> [!important] Shipped for switch profiles, and better than the wording says
> The preview of a switch profile (`POST /api/devices/{id}/switch-profile/preview`)
> is the most accomplished safeguard on this page, and it carries **four** kinds
> of warning where the design only planned one:
>
> - **per port**: what the rule asked for and the port cannot do —
>   PoE on an SFP cage, typically;
> - **per rule**: the number of ports **designated** and the number **obtained**.
>   The two differ as soon as a rule placed higher up has taken the same ports,
>   and a rule that designates thirty without winning any is not wrong, it
>   is **masked** — which is indistinguishable if only one of the
>   two numbers is displayed;
> - the ports **that no rule designates**, stated explicitly, and which remain
>   **unchanged** — a profile that does not cover everything must not suggest
>   it has dealt with everything;
> - a **wholesale** refusal when the plan carries a blocking problem: better
>   to write nothing than to write half.
>
> For **direct** application of a port profile to a selection, on the other hand,
> the only safeguard is PoE, and it is **silently corrected**: `poe: true` on
> a port without PoE becomes `poe: false`, without a warning on screen. The log
> says so ("mixed configurations"), the screen does not. It is the right behaviour — writing
> an unsatisfiable intent would create a permanent gap in [[Reconciliation]] —
> but it should be visible, as the preview knows how to do.
