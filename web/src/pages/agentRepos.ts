// Pure decisions for the Agents admin screen — kept out of the component so
// they are testable without a DOM (the house pattern: agentRepos.ts ↔
// agentRepos.test.ts).

export type AgentBuildState = "queued" | "running" | "ok" | "failed";

/** The badge tone for a build state — the same three-tone vocabulary the
 *  Drivers page uses (ok green, failed red, in-flight neutral). */
export function buildTone(state?: AgentBuildState): string {
  if (state === "failed") return "danger";
  if (state === "ok") return "ok";
  return "";
}

/** The registry reference a proxy pulls: <host>/<agent_name>:<version>.
 *
 *  Empty until there is a version to pull — a ref without one ("agent:" or
 *  "agent:latest" when nothing was built) would look pullable and 404, the
 *  exact lie the registry's own tag resolution refuses. The host is the
 *  controller as the browser reached it; a proxy on another route substitutes
 *  its own, which is why the agent_name and version — the parts that do not
 *  change with the route — are what the UI leans on. */
export function pullRef(host: string, agentName: string, version: string): string {
  if (!agentName || !version) return "";
  var h = host.replace(/\/+$/, "");
  return (h ? h + "/" : "") + agentName + ":" + version;
}

/** Whether the rebuild/enable gestures should be offered: only when the
 *  pipeline is enabled on this instance (a Docker socket is configured). */
export function canBuild(enabled: boolean): boolean {
  return enabled;
}
