// THE API REFERENCE — a page of the product, not a Swagger UI.
//
// Swagger UI would have been free and it is the wrong answer here for one
// reason that is not aesthetic: it is a SECOND interface. It arrives with its
// own typography, its own greys, its own idea of what a button looks like,
// and it renders in one theme while the product renders in two. A reader who
// reaches it has left meshcraft and is now reading a tool's opinion of
// meshcraft's API.
//
// So the material is the product's — `ui/tokens.css`, the two themes, the
// glass, the `.card` and `.badge` vocabulary every other screen uses — and
// what is borrowed from Swagger is only the part that is genuinely good: the
// verb-and-path row, grouped by resource, expanding to its payloads.
//
// The content is `apiDocsSpec.ts`; this file is the renderer. The honesty
// about what is hand-written and what is assumed lives in the first block on
// screen, not in a comment — see the `sourceTitle` banner below.

import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { ActionButton, ActionLink, useToast } from "../components";
import { Banner } from "../ui/Banner";
import { PageHeader } from "../ui/PageHeader";
import {
  countRoutes,
  filterGroups,
  GROUPS,
  QUERY_PARAMS,
  quickstart,
  REFUSALS,
  STATUS_CODES,
  type Group,
  type Route,
} from "./apiDocsSpec";

/**
 * The verb's tone. DELIBERATELY NOT THE STATUS SPINE (UI-GUIDE §2): green and
 * orange mean "this device is healthy / this device needs attention", and
 * spending them on HTTP verbs in a reference table would teach the reader's
 * eye the wrong thing on the screens where it matters.
 *
 * Three tones, and the split is what the verb DOES: a read is quiet, a write
 * is the product's blue (the same ink that means "meshcraft owes this device a
 * change"), and a delete is the one that does not come back.
 */
function verbTone(m: Route["m"]): string {
  if (m === "GET") return "read";
  if (m === "DELETE") return "drop";
  return "write";
}

function RouteRow({ r }: { r: Route }) {
  const { t } = useTranslation();
  const hasDetail = !!(r.req || r.res || r.errs?.length);
  const head = (
    <>
      <span className={"api-verb api-verb-" + verbTone(r.m)}>{r.m}</span>
      <code className="api-path">{r.p}</code>
      <span className={"badge api-gate" + (r.gate === "anon" ? " danger" : "")}>
        {t("apiDocs.gate." + r.gate)}
        {r.role ? " · " + t("common.role_" + r.role) : ""}
      </span>
    </>
  );
  const body = (
    <>
      {r.d && <p className="note api-desc">{t("apiDocs.r." + r.d)}</p>}
      {r.req && (
        <>
          <div className="api-sub">{t("apiDocs.requestBody")}</div>
          <pre className="scroll-x api-code">{r.req}</pre>
        </>
      )}
      {r.res && (
        <>
          <div className="api-sub">{t("apiDocs.responseBody")}</div>
          <pre className="scroll-x api-code">{r.res}</pre>
        </>
      )}
      {r.errs && r.errs.length > 0 && (
        <>
          <div className="api-sub">{t("apiDocs.errorsSub")}</div>
          <ul className="api-errs">
            {r.errs.map((e) => (
              <li key={e.status + e.text}>
                <span className="badge danger">{e.status}</span>{" "}
                {/* VERBATIM. A sentence produced by the backend is displayed
                    as it was produced — the same rule as a connector refusal
                    on the device screens. Translating it would mean the
                    operator and the server log disagree about what happened. */}
                <code>{e.text}</code>
              </li>
            ))}
          </ul>
        </>
      )}
    </>
  );

  // A row with nothing to expand must not pretend it has: a <details> that
  // opens onto a single line of prose is a gesture that buys nothing.
  if (!hasDetail) {
    return (
      <div className="api-route api-route-flat">
        <div className="api-route-head">{head}</div>
        {r.d && <p className="note api-desc">{t("apiDocs.r." + r.d)}</p>}
      </div>
    );
  }
  return (
    <details className="api-route">
      <summary className="api-route-head">{head}</summary>
      <div className="api-route-body">{body}</div>
    </details>
  );
}

function RouteGroup({ g }: { g: Group }) {
  const { t } = useTranslation();
  return (
    <section className="card api-group" id={"api-" + g.id}>
      <div className="card-head">
        <h3>{t("apiDocs.g." + g.id)}</h3>
        <span className="note">{t("apiDocs.routeCount", { count: g.routes.length })}</span>
      </div>
      <p className="note">{t("apiDocs.gd." + g.id)}</p>
      <div className="api-routes">
        {g.routes.map((r) => (
          <RouteRow key={r.m + r.p} r={r} />
        ))}
      </div>
    </section>
  );
}

export default function ApiDocs() {
  const { t } = useTranslation();
  const [toast, showToast] = useToast();
  const [query, setQuery] = useState("");
  const groups = useMemo(() => filterGroups(GROUPS, query), [query]);
  const origin = window.location.origin;
  const qs = quickstart(origin);

  return (
    <div className="page apidocs">
      <PageHeader title={t("apiDocs.title")} subtitle={t("apiDocs.subtitle")}>
        <ActionLink kind="role" to="/profile" className="secondary">
          {t("apiDocs.myTokens")}
        </ActionLink>
      </PageHeader>
      {toast}

      {/* WHERE THIS DOCUMENT COMES FROM, FIRST — before anything a reader
          could mistake for a guarantee. */}
      <Banner
        tone="info"
        title={t("apiDocs.sourceTitle")}
        note={t("apiDocs.sourceNote")}
        actions={
          <>
            <ActionLink kind="export" to="/api/v1/contracts/mdp" reloadDocument className="ghost">
              {t("apiDocs.mdpSpec")}
            </ActionLink>
            <ActionLink kind="export" to="/api/v1/contracts/agent" reloadDocument className="ghost">
              {t("apiDocs.agentSpec")}
            </ActionLink>
          </>
        }
      >
        <p className="note">{t("apiDocs.sourceCaveat")}</p>
      </Banner>

      <nav className="api-toc" aria-label={t("apiDocs.tocLabel") as string}>
        <a href="#api-auth">{t("apiDocs.authTitle")}</a>
        <a href="#api-security">{t("apiDocs.securityTitle")}</a>
        <a href="#api-conventions">{t("apiDocs.conventionsTitle")}</a>
        <a href="#api-quickstart">{t("apiDocs.quickstartTitle")}</a>
        <a href="#api-routes">{t("apiDocs.routesTitle")}</a>
      </nav>

      {/* ---------------------------------------------------- authentication */}
      <section className="card" id="api-auth">
        <div className="card-head">
          <h3>{t("apiDocs.authTitle")}</h3>
        </div>
        <p>{t("apiDocs.authBody")}</p>
        {/* The real shape, not an ellipsis: `mct_<selector>_<secret>`. The
            head before the second underscore is the `prefix` the list shows —
            it identifies the token and cannot authenticate it. */}
        <pre className="scroll-x api-code">
          {`Authorization: Bearer mct_1a2b3c4d_9f8e7d6c…`}
        </pre>
        <Banner tone="act" title={t("apiDocs.neverCookieTitle")}>
          <p className="note">{t("apiDocs.neverCookieBody")}</p>
        </Banner>
        <div className="api-sub">{t("apiDocs.canTitle")}</div>
        <ul className="api-list">
          <li>{t("apiDocs.can1")}</li>
          <li>{t("apiDocs.can2")}</li>
          <li>{t("apiDocs.can3")}</li>
        </ul>
        <div className="api-sub">{t("apiDocs.cannotTitle")}</div>
        <ul className="api-list">
          <li>{t("apiDocs.cannot1")}</li>
          <li>{t("apiDocs.cannot2")}</li>
          <li>{t("apiDocs.cannot3")}</li>
          <li>{t("apiDocs.cannot4")}</li>
          {/* The two gestures `auth.CookieOnly` refuses. Stated here rather
              than left to be discovered as a 403, because it is the one
              refusal a script author cannot work around and must design
              around. */}
          <li>{t("apiDocs.cannot5")}</li>
        </ul>

        {/* THE REFUSALS, IN THE SECTION ABOUT AUTHENTICATING — which is where
            somebody holding a 401 is already looking. The sentences are the
            server's own, verbatim; the gloss beside each is ours. */}
        <div className="api-sub">{t("apiDocs.refusalsTitle")}</div>
        <p className="note">{t("apiDocs.refusalsIntro")}</p>
        <div className="scroll-x">
          <table>
            <thead>
              <tr>
                <th>{t("apiDocs.codeCol")}</th>
                <th>{t("apiDocs.errorsSub")}</th>
                <th>{t("apiDocs.meaningCol")}</th>
              </tr>
            </thead>
            <tbody>
              {REFUSALS.map((rf) => (
                <tr key={rf.text}>
                  <td>
                    <span className={"badge " + (rf.status < 403 ? "warn" : "danger")}>
                      {rf.status}
                    </span>
                  </td>
                  <td>
                    <code className="api-refusal">{rf.text}</code>
                  </td>
                  <td>{t(rf.d)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>

        <p className="note">{t("apiDocs.adrPointer")}</p>
      </section>

      {/* -------------------------------------------------------- security */}
      <section className="card api-security" id="api-security">
        <div className="card-head">
          <h3>{t("apiDocs.securityTitle")}</h3>
        </div>
        <p>{t("apiDocs.securityIntro")}</p>

        <div className="api-sub">{t("apiDocs.dontTitle")}</div>
        <ul className="api-dont">
          <li>{t("apiDocs.dont1")}</li>
          <li>{t("apiDocs.dont2")}</li>
          <li>{t("apiDocs.dont3")}</li>
          <li>{t("apiDocs.dont4")}</li>
          <li>{t("apiDocs.dont5")}</li>
          <li>{t("apiDocs.dont6")}</li>
        </ul>

        <div className="api-sub">{t("apiDocs.browserTitle")}</div>
        <p className="note">{t("apiDocs.browserBody")}</p>

        <div className="api-sub">{t("apiDocs.doTitle")}</div>
        <ul className="api-list">
          <li>{t("apiDocs.do1")}</li>
          <li>{t("apiDocs.do2")}</li>
          <li>{t("apiDocs.do3")}</li>
          <li>{t("apiDocs.do4")}</li>
        </ul>

        <Banner
          tone="act"
          title={t("apiDocs.leakTitle")}
          note={t("apiDocs.leakIntro")}
          items={[
            t("apiDocs.leak1"),
            t("apiDocs.leak2"),
            t("apiDocs.leak3"),
            t("apiDocs.leak4"),
          ]}
          actions={
            <ActionLink kind="role" to="/profile">
              {t("apiDocs.goRevoke")}
            </ActionLink>
          }
        />
      </section>

      {/* ----------------------------------------------------- conventions */}
      <section className="card" id="api-conventions">
        <div className="card-head">
          <h3>{t("apiDocs.conventionsTitle")}</h3>
        </div>
        <dl className="api-facts">
          <dt>{t("apiDocs.baseUrl")}</dt>
          <dd>
            <code>{origin}</code>
          </dd>
          <dt>{t("apiDocs.contentType")}</dt>
          <dd>
            <code>application/json</code>
          </dd>
          <dt>{t("apiDocs.versioning")}</dt>
          <dd>{t("apiDocs.versioningBody")}</dd>
          <dt>{t("apiDocs.pagination")}</dt>
          <dd>{t("apiDocs.paginationBody")}</dd>
          <dt>{t("apiDocs.tabHeader")}</dt>
          <dd>{t("apiDocs.tabHeaderBody")}</dd>
        </dl>

        <div className="api-sub">{t("apiDocs.envelopeTitle")}</div>
        <p className="note">{t("apiDocs.envelopeBody")}</p>
        <pre className="scroll-x api-code">{`{ "error": "insufficient rights on this site" }`}</pre>
        <p className="note">{t("apiDocs.envelopeQuirk")}</p>

        <div className="api-sub">{t("apiDocs.codesTitle")}</div>
        <div className="scroll-x">
          <table>
            <thead>
              <tr>
                <th>{t("apiDocs.codeCol")}</th>
                <th>{t("apiDocs.meaningCol")}</th>
              </tr>
            </thead>
            <tbody>
              {STATUS_CODES.map((c) => (
                <tr key={c}>
                  <td>
                    <span className={"badge " + (c < 300 ? "ok" : c < 500 ? "warn" : "danger")}>
                      {c}
                    </span>
                  </td>
                  <td>{t("apiDocs.code." + c)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>

        <div className="api-sub">{t("apiDocs.queryTitle")}</div>
        <p className="note">{t("apiDocs.queryBody")}</p>
        <div className="scroll-x">
          <table>
            <thead>
              <tr>
                <th>{t("apiDocs.paramCol")}</th>
                <th>{t("apiDocs.appliesCol")}</th>
              </tr>
            </thead>
            <tbody>
              {QUERY_PARAMS.map((p) => (
                <tr key={p.name}>
                  <td>
                    <code>{p.name}</code>
                  </td>
                  <td>
                    <code className="api-param-on">{p.on}</code>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>

      {/* ------------------------------------------------------ quickstart */}
      <section className="card" id="api-quickstart">
        <div className="card-head">
          <h3>{t("apiDocs.quickstartTitle")}</h3>
          <ActionButton
            kind="copy"
            className="secondary"
            onClick={() => {
              void navigator.clipboard.writeText(qs);
              showToast(t("apiDocs.copied"));
            }}
          >
            {t("apiDocs.copyQuickstart")}
          </ActionButton>
        </div>
        <p className="note">{t("apiDocs.quickstartNote")}</p>
        <pre className="scroll-x api-code">{qs}</pre>
      </section>

      {/* ---------------------------------------------------------- routes */}
      <section id="api-routes">
        <div className="toolbar api-routes-bar">
          <h2>{t("apiDocs.routesTitle")}</h2>
          <input
            className="api-search"
            value={query}
            autoComplete="off"
            spellCheck={false}
            aria-label={t("apiDocs.searchLabel") as string}
            placeholder={t("apiDocs.searchPlaceholder") as string}
            onChange={(e) => setQuery(e.target.value)}
          />
          {query !== "" && (
            <ActionButton kind="cancel" className="ghost" onClick={() => setQuery("")}>
              {t("common.clear")}
            </ActionButton>
          )}
        </div>
        <p className="note">
          {t("apiDocs.showing", { count: countRoutes(groups) })}
        </p>
        {groups.length === 0 ? (
          <p className="empty">{t("apiDocs.noRouteMatch")}</p>
        ) : (
          groups.map((g) => <RouteGroup key={g.id} g={g} />)
        )}
      </section>
    </div>
  );
}
