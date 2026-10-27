import { useTranslation } from "react-i18next";
import { HelpTip, QuickTip } from "../../components";
// The five verdicts on the name the EQUIPMENT carries, kept out of React
// (hostnameConformity.test.ts). The one this row must never blur is
// `unsupported`: a platform meshcraft cannot rename will never converge, so its
// difference is not a repair anyone is waiting for.
import {
  hostnameSourceKey,
  hostnameVerdict,
  type HostnameConformity,
} from "../hostnameConformity";

/**
 * DOES THE EQUIPMENT ANSWER TO ITS NAME — a verdict, in the row slot the old
 * "push the name" button used to occupy.
 *
 * There is no gesture left. The name is reconciled: renaming the device (the
 * title input in the header) writes the name onto the equipment, and a backend
 * loop repairs drift on its own. So this row REPORTS, and the only thing it can
 * get wrong is which of five sentences it tells — hence the mapping in
 * hostnameConformity.ts, where the distinction that matters is tested:
 * "waiting to be renamed" and "this range cannot be renamed by meshcraft"
 * must never render alike, because the second will never converge.
 *
 * Shown to EVERY role and on an unreachable device too. This is not a control
 * that could fail, it is an observation, and it is precisely on the device
 * nobody can reach that "nothing has reported this yet" is worth reading.
 *
 * No "repair now" button next to `drift`, deliberately: the repair is already
 * queued, and offering to trigger it would spend a session on ranges that grant
 * five to do what the loop was about to do for free.
 *
 * RENDERS NOTHING when there is nothing honest to say. Both halves may be
 * absent, and neither absence is "the names agree": a controller that said
 * nothing, and a state this frontend does not know, both render NOTHING.
 * Silence is the only honest answer about a comparison we cannot report. (On
 * the two field spellings, cf. Device.hostname in api.ts.)
 *
 * A pair of `kv` grid cells in a fragment, not a wrapper: the grid is the
 * summary panel's, and a box around these two would break the column it lives
 * in.
 */
export function HostnameRow({ conf }: { conf: HostnameConformity | undefined }) {
  const { t } = useTranslation();
  const said = hostnameVerdict(conf?.state);
  const src = hostnameSourceKey(conf?.source);
  if (!conf || !said) return null;
  return (
    <>
      <span className="k" style={{ display: "inline-flex", alignItems: "center", gap: 4 }}>
        {t("switchDetailPage.hostnameLabel")}
        <HelpTip label={t("switchDetailPage.hostnameHelpLabel")}>
          <p>{t("switchDetailPage.hostnameHelp1")}</p>
          <p>{t("switchDetailPage.hostnameHelp2")}</p>
        </HelpTip>
      </span>
      {/* The controller's reason rides along as the HOVER TEXT whenever the
          verdict does not print it (the tolerant match — see
          hostnameConformity). QuickTip falls through transparently on an
          empty string, so the states that DO print their reason are
          untouched. */}
      <QuickTip
        text={!said.showBecause && conf.because ? conf.because : ""}
        className="quicktip-inline"
      >
      <span style={{ color: said.color }}>
        {t(`switchDetailPage.${said.sentenceKey}`, {
          desired: conf.desired,
          observed: conf.observed ?? "",
        })}
        {/* The name it DOES answer to, as its own fragment: only
            where the sentence did not already name both (i.e. not
            on drift, whose whole content is the comparison). */}
        {said.observedFragment && conf.observed
          ? " " + t("switchDetailPage.hostnameObservedIs", { observed: conf.observed })
          : ""}
        {/* WHERE THE READING CAME FROM. An operator asking why
            meshcraft thinks this switch is called something needs
            the source: the LLDP self-report costs no session and
            rides the SNMP poll, a connector read means someone
            logged in. Absent when the controller did not say —
            never guessed. */}
        {said.showSource && src ? (
          <span className="note">
            {" (" + t(`switchDetailPage.${src}`) + ")"}
          </span>
        ) : null}
        {/* The controller's own sentence, VERBATIM. On
            `unnameable` it names the offending character, which is
            the only detail that makes the rename possible —
            paraphrasing it here would throw that away. */}
        {said.showBecause && conf.because ? (
          // A block-styled SPAN, not a div: this sits inside the
          // value span of the kv grid, where a div is invalid
          // nesting — tolerated by browsers, and the kind of thing
          // that breaks the day the grid is restyled.
          <span className="note" style={{ display: "block", marginTop: 2 }}>
            {conf.because}
          </span>
        ) : null}
      </span>
      </QuickTip>
    </>
  );
}
