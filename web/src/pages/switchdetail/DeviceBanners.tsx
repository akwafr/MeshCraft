import { useTranslation } from "react-i18next";
import type { Device } from "../../api";
import {
  ActionButton,
  ConnectorNotesBanner,
  DriftBanner,
  FailedApplyBanner,
  IncompleteApplyBanner,
  RefusedBanner,
  SkippedBanner,
  UnsupportedBanner,
} from "../../components";
import { ConformMgmtButton } from "../ConformMgmt";
import { Banner } from "../../ui/Banner";
import { StpRegionBanner } from "./StpRegionBanner";

/**
 * THE BANNER STACK across the top of the switch sheet, and its ORDER is the
 * whole point of keeping it in one place.
 *
 * Reading down, the sequence goes from "meshcraft no longer knows anything
 * about this device" to "here is what the device actually does with traffic":
 * the ones that report a LOST FACULTY first (nobody watches it, nobody collects
 * it), then the ones that demand a DECISION (an apply failed, ports were
 * skipped), then the ones that merely inform. A banner moved up or down that
 * list changes which sentence an operator reads first at three in the morning,
 * so the reasons live next to the JSX rather than in a commit message.
 *
 * Every banner is conditional and most render nothing most of the time; the
 * stack collapses to nothing on a healthy device.
 */
export function DeviceBanners({
  dev,
  canEdit,
  off,
  collectorStranded,
  busy,
  onApply,
  onImport,
  onToast,
  onViewMgmtPlan,
}: {
  dev: Device;
  /** site_admin: the only role offered the repair gestures. */
  canEdit: boolean;
  /**
   * The device is unreachable (`dev.status === "off"`), the same flag that
   * dims the faceplate. Here it only reaches the connector's remarks: they
   * describe the last READING, which on an unreachable device is history
   * rather than state, so they fold instead of heading the page.
   */
  off: boolean;
  /** Attached to a proxy that no longer covers this site — cf. the banner. */
  collectorStranded: boolean;
  /** An apply or an import is in flight; every action in the stack waits. */
  busy: boolean;
  /** `force` replays the apply over the ports the connector skipped. */
  onApply: (force: boolean) => void;
  onImport: () => void;
  onToast: (msg: string) => void;
  onViewMgmtPlan: () => void;
}) {
  const { t } = useTranslation();
  // The structured entries split by what they invite: guardrails join the
  // forceable banner, everything else is final and gets no force button.
  const notApplied = dev.last_apply?.not_applied ?? [];
  const asLine = (n: { port?: string; text: string }) =>
    n.port ? `${n.port}: ${n.text}` : n.text;
  const forceable = [
    ...(dev.last_apply?.skipped ?? []),
    ...notApplied.filter((n) => n.kind === "guardrail").map(asLine),
  ];
  const finalRefusals = notApplied.filter((n) => n.kind !== "guardrail").map(asLine);
  return (
    <>
      {dev.mgmt?.checked && !dev.mgmt.conforms && (
        // Outside the site addressing plan: meshcraft still watches the
        // device, but administration is not on the planned path yet.
        // Same visual family as config drift — an actionable gap, not a scare.
        <Banner
          tone="act"
          className="mgmt-warn-banner"
          title={t("switchDetailPage.offMgmtPlan")}
          note={dev.mgmt.reason ?? t("switchDetailPage.defaultMgmtUnreachable")}
          actions={
            <>
              {canEdit && <ConformMgmtButton device={dev} onToast={onToast} />}
              <ActionButton
                kind="link"
                className="ghost"
                title={t("switchDetailPage.openMgmtPlanTitle")}
                onClick={onViewMgmtPlan}
              >
                {t("switchDetailPage.viewPlan")}
              </ActionButton>
            </>
          }
        >
          {dev.mgmt.path_state && dev.mgmt.path_state !== "ready" && (
            <div className="note warn-text">{dev.mgmt.path_reason}</div>
          )}
        </Banner>
      )}
      {/* Equipment no one monitors anymore: the indicator vanished, and without
          this banner you would read controller logs to learn why. Placed at the
          very top — the only one of these banners that says meshcraft no longer
          knows anything about the device. */}
      {dev.watch_excluded && (
        <Banner
          tone="act"
          title={t("switchDetailPage.noLongerWatchedTitle")}
          note={dev.watch_excluded}
        >
          <div className="note">{t("switchDetailPage.watchExcludedNote")}</div>
        </Banner>
      )}
      {/* Just after: attachment became non-covering. Same family as the banner
          above — explain why an indicator vanished — but one notch less severe,
          because the device is still pinged and its status remains true. That
          is what makes it treacherous: nothing on screen would betray lost SNMP
          collection without this banner.

          No server-side equivalent, and there cannot be one: the controller
          refuses to WRITE a non-covering attachment; it cannot prevent coverage
          from shrinking afterward. */}
      {collectorStranded && (
        <Banner
          tone="act"
          title={t("switchDetailPage.noSnmpCollectorTitle")}
          note={t("switchDetailPage.collectorStrandedNote1", { name: dev.proxy_name || t("switchDetailPage.unknownProxy") })}
        >
          <div className="note">{t("switchDetailPage.collectorStrandedNote2")}</div>
        </Banner>
      )}
      {/* FIRST: failure is the only banner that signals nothing is in place. It
          must come before nuances. */}
      {dev.last_apply?.error && (
        <FailedApplyBanner
          error={dev.last_apply.error}
          busy={busy}
          onRetry={() => onApply(false)}
          onImport={onImport}
        />
      )}
      {forceable.length > 0 && (
        <SkippedBanner
          skipped={forceable}
          busy={busy}
          onForce={() => onApply(true)}
          onCancel={onImport}
        />
      )}
      {/* THE SPANNING-TREE REGION, and its place in the order is an argument
          rather than an afterthought.

          It comes AFTER failure and skipped ports, because those two say a
          write did not land at all and that outranks a nuance. It comes BEFORE
          every informative banner, because it is the only one that says this
          switch is not in the tree the site designed — and 802.1s makes that
          state look perfectly healthy: the ports forward, the applies return
          `ok`, and the sole symptom is a port blocking somewhere nobody
          expected. On ARENA it stayed invisible for weeks (ADR-0021).

          Renders nothing on a conformant device, on a site that does not use
          MSTP, and on a device never read — cf. StpRegionBanner. */}
      <StpRegionBanner
        dev={dev}
        canEdit={canEdit}
        busy={busy}
        onToast={onToast}
      />
      {/* After spared-port strip: this one calls for no decision, so it must not
          come before one that does. */}
      <RefusedBanner notes={finalRefusals} />
      <UnsupportedBanner notes={dev.last_apply?.unsupported ?? []} />
      {/* LAST informative banner: describes neither failure nor refusal nor a
          platform limit — it describes what the device ACTUALLY does with
          traffic, re-read after apply. The controller produced these sentences
          from the start and nothing displayed them: a VLAN configuration can be
          applied, verified by re-read, counted conformant, and inert.

          FOLDED when the device is unreachable: on a device nobody reaches,
          what the connector read last apply is the last thing known about
          it — worth keeping for diagnosis, not worth the top of a screen
          whose only news is that it is gone. */}
      <ConnectorNotesBanner notes={dev.last_apply?.notes ?? []} collapsed={off} />

      {/* "In progress" but blocked: apply ran and did not finish everything.
          Reserved for `pending` — true drift has its own banner; showing two for
          one problem would blur both. */}
      {dev.sync === "pending" && dev.apply_stalled && (
        <IncompleteApplyBanner
          changes={dev.drift ?? []}
          busy={busy}
          onRetry={() => onApply(false)}
          onImport={onImport}
        />
      )}
      {dev.sync === "drift" && (
        <DriftBanner
          changes={dev.drift ?? []}
          busy={busy}
          onImport={onImport}
          onForce={() => onApply(false)}
        />
      )}
    </>
  );
}
