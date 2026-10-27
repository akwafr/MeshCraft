import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { post, type Device, type STPRegionConformity } from "../../api";
import { ActionButton } from "../../components";
import { Banner } from "../../ui/Banner";
import { ConfirmDialog } from "../../ui/ConfirmDialog";

type ApplyReply = {
  ok: boolean;
  mode?: string;
  notes?: string[];
  skipped?: string[];
};

/**
 * THE SPANNING-TREE BANNER (ADR-0023), and the whole point of it is that it
 * says WHICH of the three values diverges.
 *
 * Until ADR-0021 the MST region appeared on no screen at all, and that is how a
 * site rename left seven Instant On of ARENA in region `site-exec` while
 * the site wanted another — for weeks, with no symptom, every port forwarding
 * and every apply returning `ok`. A banner saying "the configuration differs"
 * would reproduce the same blindness one level up: the operator still could not
 * tell a renamed region from an added VLAN, and the two are not repaired the
 * same way.
 *
 * So it names the name, the revision and the VLAN-to-instance table — the three
 * components 802.1s compares, all three or nothing — on both sides.
 *
 * # THE TONE IS NOT ALWAYS `act`, and that is the guide's rule rather than a
 * # softening
 *
 * `act` when "Conformer" is offered: there is a decision to take. `info` when
 * the connector cannot join a region at all (fs-s3400 declares STP writable and
 * writes no region — ADR-0022), because UI-GUIDE §2 reserves orange for what
 * calls for a gesture, and an orange banner carrying no gesture for months is
 * the definition of what teaches people to ignore orange. The fact is still
 * stated, in full, at the top of the device sheet.
 *
 * # NO DISMISS BUTTON. EVER.
 *
 * A banner that can be hidden is a drift nobody will see again, which is the
 * state before ADR-0021 with a checkbox on top. Ignoring it is a legitimate
 * operational choice — the addendum of ADR-0021 records a real one, "no STP
 * apply on those seven without planning it" — and that choice is made by
 * READING the banner, not by silencing it.
 */
export function StpRegionBanner({
  dev,
  canEdit,
  busy,
  onToast,
}: {
  dev: Device;
  /** site_admin: the only role offered the repair gesture. */
  canEdit: boolean;
  /** An apply or an import is in flight elsewhere on the sheet. */
  busy: boolean;
  onToast: (msg: string) => void;
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [confirming, setConfirming] = useState(false);
  const r = dev.stp_region;

  const conform = useMutation({
    mutationFn: () =>
      // `mode: "mstp"` and NOTHING ELSE. `super_root` is deliberately omitted:
      // the controller reads an absent flag as "leave the stored intent alone"
      // (a three-way pointer, cf. handleApplyDeviceSTP), and sending `false`
      // here would un-elect a region root while conforming a region.
      post<ApplyReply>(`/api/devices/${dev.id}/stp/apply`, { mode: "mstp" }),
    onSuccess: (reply) => {
      qc.invalidateQueries({ queryKey: ["devices", dev.site_id] });
      setConfirming(false);
      // Connector sentences VERBATIM, notes and refusals with the same weight:
      // a `Skipped` line is the only proof the operator was told a region was
      // not written (ADR-0022), and folding it into a cheerful toast is the
      // silent success this whole chain exists to stop.
      const said = [...(reply.notes ?? []), ...(reply.skipped ?? [])];
      onToast(said.length ? said.join(" · ") : t("stpRegion.conformedToast"));
    },
    onError: (e: Error) => {
      setConfirming(false);
      onToast(e.message || t("stpRegion.conformFailed"));
    },
  });

  if (!r || r.verdict === "match") return null;

  // GATED ON THREE FACTS, not one. `can_join` is the connector declaring it
  // emits a region (ADR-0022); `write` is the connector declaring its STP
  // writes reach a device at all; `mstp` in the mode list is the range being
  // able to run the protocol the region belongs to. Any of the three missing
  // and the button would be offered where the endpoint answers 422 or where it
  // would land and do nothing.
  const platformCanConform =
    r.can_join &&
    dev.stp_caps?.write === true &&
    (dev.stp_caps?.modes ?? []).includes("mstp");
  const offer = platformCanConform && canEdit;

  // THREE TITLES, one per verdict, because they send the reader to three
  // different places: a rename, a region that was never configured, and a
  // switch that is not even speaking the protocol.
  const titleKey =
    r.verdict === "not_mstp"
      ? "stpRegion.titleNotMstp"
      : r.verdict === "unregioned"
        ? "stpRegion.titleUnregioned"
        : "stpRegion.titleDrift";

  return (
    <>
      <Banner
        tone={offer ? "act" : "info"}
        title={t(titleKey)}
        note={t("stpRegion.identityNote")}
        items={componentLines(r, t)}
        actions={
          offer ? (
            <ActionButton
              kind="apply"
              className="secondary"
              disabled={busy || conform.isPending}
              title={t("stpRegion.conformTitle")}
              onClick={() => setConfirming(true)}
            >
              {conform.isPending
                ? t("stpRegion.conforming")
                : t("stpRegion.conform")}
            </ActionButton>
          ) : undefined
        }
      >
        {/* The reason there is no button, and it is two different reasons that
            must not read as one: a platform that cannot, versus a reader who
            may not. "Ask someone with rights" is useless advice when the
            CONNECTOR is what cannot write — platform first, permission
            second, same order as the STP card's own refusal. */}
        {!platformCanConform ? (
          <div className="note">{t("stpRegion.cannotJoinNote")}</div>
        ) : !canEdit ? (
          <div className="note">{t("stpRegion.readOnlyNote")}</div>
        ) : null}
        {/* A comparison is only as fresh as its reading. Said out loud because
            the inverse mistake is the expensive one: no banner over a switch
            nobody has read is not a clean bill of health (ADR-0023). */}
        <div className="note">{t("stpRegion.observedNote")}</div>
      </Banner>
      {confirming && (
        <ConfirmDialog
          title={t("stpRegion.confirmTitle", { name: dev.name })}
          confirmKind="apply"
          confirmLabel={t("stpRegion.confirmButton")}
          busy={conform.isPending}
          onCancel={() => setConfirming(false)}
          onConfirm={() => conform.mutate()}
          body={
            <>
              {/* THE PRICE, MEASURED AND NAMED — not "this may be disruptive".
                  Moving an MST boundary recalculates the topology: 2-5 s
                  unreachable and about 45 s to settle were measured on this
                  fleet, and core01-arena is CIST root at priority 0 with ten
                  boundary ports (ADR-0022). An operator who reads a vague
                  warning schedules nothing. */}
              <p>{t("stpRegion.confirmCost")}</p>
              <p>
                {t("stpRegion.confirmIdentity", {
                  want: r.want_name || "—",
                  have: r.have_name || "—",
                })}
              </p>
              {r.verdict === "not_mstp" && (
                <p>{t("stpRegion.confirmModeChange")}</p>
              )}
            </>
          }
        />
      )}
    </>
  );
}

/**
 * The three components, each as one sentence: what the site wants against what
 * the switch carries, and a mark on the ones that disagree.
 *
 * The instance rows are built from the UNION of both sides. Listing only what
 * the site wants would hide the interesting half of the ARENA case — a switch
 * carrying an instance the site has forgotten is as far out of the region as
 * one missing a VLAN, and the digest does not care which direction the
 * difference goes.
 */
function componentLines(
  r: STPRegionConformity,
  t: (key: string, opts?: Record<string, unknown>) => string,
): ReactNode[] {
  const differs = new Set(r.differs ?? []);
  // Each row is ONE translated sentence with its values interpolated — never
  // fragments joined in JS, because word order does not survive translation.
  // The "differs" mark is an adjacent annotation rather than a piece of the
  // sentence, which is the same idiom the STP card already uses for its
  // separators.
  const mark = (on: boolean) =>
    on ? <span className="warn-text"> · {t("stpRegion.differs")}</span> : null;

  const wantByInst = new Map(r.want_instances.map((i) => [i.instance, i.vlans]));
  const haveByInst = new Map(r.have_instances.map((i) => [i.instance, i.vlans]));
  const ids = [...new Set([...wantByInst.keys(), ...haveByInst.keys()])].sort(
    (a, b) => a - b,
  );
  const vlans = (v: number[] | undefined) =>
    v && v.length > 0 ? v.join(", ") : t("stpRegion.none");

  const lines: ReactNode[] = [
    <>
      {t("stpRegion.rowName", {
        want: r.want_name || "—",
        have: r.have_name || "—",
      })}
      {mark(differs.has("name"))}
    </>,
    <>
      {t("stpRegion.rowRevision", {
        want: r.want_revision,
        have: r.have_revision,
      })}
      {mark(differs.has("revision"))}
    </>,
  ];
  if (ids.length === 0) {
    lines.push(
      <>
        {t("stpRegion.rowTableEmpty")}
        {mark(differs.has("instances"))}
      </>,
    );
  }
  for (const id of ids) {
    lines.push(
      <>
        {t("stpRegion.rowInstance", {
          id,
          want: vlans(wantByInst.get(id)),
          have: vlans(haveByInst.get(id)),
        })}
        {mark(differs.has("instances"))}
      </>,
    );
  }
  return lines;
}
