---
Status: Accepted
Date: 2026-10-24
---

# ADR-0017 — Rejecting a short read belongs to absorption

## Context

`Server.absorbObserved` brings a device's **observed** configuration into meshcraft's
intent. It replaces `devices.ports` **wholesale**: a partial observation does not
merge, it **deletes** the intent of every port the read did not reach.

Two incidents, and an instruction that did not hold.

**2026-08-11, `poefs3400lab`.** An observation of 23 ports out of 52 was persisted:
`show running-config` was cut in the middle of GE23's stanza, and the connector
accepted it because it only considers a read failed if the output is **empty**. The
"import" button was offered. Pressing it would have erased 29 ports, including the four
10G trunks. Fix: a refusal in `handleImportDeviceConfig`, measured against the
**faceplate** — which comes from the model, not from the read that may have been
truncated.

And, in `absorbObserved`'s header, an instruction in capitals:

> THE CALLER OWNS THE SAFETY CHECK … any new caller has to make the same
> refusal before arriving here.

**2026-10-24, `sw01-arena`.** That new caller arrived: `importFromDevice`, the import
at adoption. It only tested `len(state.Ports) == 0`. Adoption of an FS S3400-48T4SP,
faceplate **52 ports** (48 × GE + 4 × XG 10G):

- **48 ports imported out of 52.** The four uplinks `XG1`–`XG4` are absent from the
  model.
- The 48 are **all `access`** on a switch that carries 7 VLANs — and it is precisely
  the uplink that is missing.
- **`sync: in_sync`.** Intent and observation come out of the **same** truncated read,
  so drift compares nothing: both are equally wrong. Nothing will ever flag it.

An `apply` on this device would treat the four 10G ports as unconfigured.

The fact that decides this ADR is not the incident, it is the instruction: it was
written, explicit, in capitals, in the function itself — and the second caller was
written without it. It did not hold **even once**. It is the same pattern as the bug
found earlier the same evening in `parse.go`: the rule is in a comment, so it depends
on the goodwill of someone who reads, and it copies badly.

## Decision

**The check moves down into `absorbObserved`, and adoption on a short read adopts
without importing.**

1. **The refusal is in the function**, executed **before any write and before any
   database read**. No caller can get beyond it. The instruction "every new caller must
   redo this refusal" is removed because it no longer has a purpose.

2. **The refusal is a type**, `*shortRead`, not a boolean or a string. Each caller still
   chooses its rendering — 422 on the device page, a warning sentence at adoption — but
   the **default of forgetting has switched sides**: a caller that ignores this case
   gets a refusal and a propagated error, no longer a silent deletion. Safety no longer
   depends on what the caller read, only the quality of the message does.

3. **An observation with no ports at all is refused too**, whatever the faceplate, as
   soon as the device's model already carries some. Without this clause the function
   would remain safe only on platforms that announce a faceplate, and the guard for the
   other half would stay with the caller — which is what this ADR undoes.

4. **At adoption, a short read adopts the device and imports nothing.** The device
   exists, its port model stays **empty**, and the sentence says what to do. Nothing
   false is asserted.

Point 4 is the real trade-off, and it reads on a single question: what is the most
repairable? An **empty** model is visibly empty — the faceplate is drawn, the ports are
free, the operator re-reads and imports from the device page. A **truncated** model
asserts `in_sync`: it is wrong and it claims to be right, and nobody comes to repair
what the product declares compliant.

## Consequences

- **A device adopted on a short read carries an empty model**, and the operator has one
  more gesture to make. That is the price, accepted: adoption succeeded, and the missing
  gesture is visible.
- **The faceplate becomes load-bearing for import.** A platform that declared a
  faceplate that is **too large** would block the import of a healthy device. Measured
  on the fleet before the first refusal: eight devices out of nine observe **exactly**
  their faceplate count, the ninth observes **more** (CiscoLabFirst1, 28 against 26
  declared). `>` is therefore deliberately not refused, and an unknown faceplate (`0`)
  blocks nothing — otherwise every platform that does not announce one would lose
  import.
- **A new caller remains free to render the message badly.** The type guarantees it will
  destroy nothing; it does not guarantee the operator will read a useful sentence rather
  than "internal error". That is the accepted residue: we moved safety, not ergonomics.
- **What is already in the database is not repaired, and it is not detected by this
  fix.** `sw01-arena` carries today an intent of 48 ports for a faceplate of 52, with
  `sync: in_sync`, and will keep it: drift compares intent with observation, which come
  from the same read. Detecting it requires comparing the **intent's port count** with
  the faceplate — a model consistency check, not drift. It is not done here, and **no
  data is modified in the database**.

## Rejected alternatives

**Copy the three lines into `importFromDevice`.** The minimal fix, and the one that will
be proposed. Rejected because **duplication is precisely what failed**: the instruction
existed, in capitals, and the second caller went past it. The third would pay the same
price, and tonight's incident is the measure of what this option costs.

**Keep the check with the caller and strengthen the comment.** Same reason. Nothing
enforces a comment. The repository has very long and very good ones — that one was good,
and it served no purpose.

**Make `absorbObserved` a function with a mandatory `faceplate int` parameter.**
Seriously considered: the compiler would force every caller to supply the yardstick.
Rejected for two reasons. A caller can pass `0` to silence the check, and the only thing
known about this function's callers is that one of them will take the shortest path.
Above all, `faceplatePortCount(d)` **derives** from the `model.Device` that
`absorbObserved` already receives: asking the caller for a value the function can
compute opens the door to it supplying a different one.

**Refuse the whole adoption on a short read.** The safest in appearance. Rejected
because the device **does not get in at all**: the operator adopting a rack loses the
one whose read was cut, and a refused adoption is the only state from which the
interface no longer allows anything — the candidate stays a candidate, whereas the short
read is repaired by re-reading.

**Adopt and import the 48 anyway, with a warning.** That is today's behaviour, plus a
sentence. Rejected because the warning lives in the adoption response, is read once, and
the wrong model remains — asserting `in_sync`. A warning does not cancel a false
assertion.

**Import the short read and mark the device "partial".** It would need a third sync
state, understood by drift, apply, compliance and the interface. Expensive, and the
result is that we keep a wrong model in the database in order to decorate it.

**Compare the observed count with the existing intent** (refuse any absorption that
shrinks the set of ports) rather than with the faceplate. Rejected: a legitimately
re-modelled device — ports renamed, stack member removed — would be blocked with no way
out. The faceplate has the property the intent lacks: it comes from the **model**, not
from the read that may have been cut.
