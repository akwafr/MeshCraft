import { Navigate, Route, Routes, useParams } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useAuth } from "./auth";
import Layout from "./Layout";
import Login from "./pages/Login";
import Dashboard from "./pages/Dashboard";
import Equipements from "./pages/Equipements";
import Ports from "./pages/Ports";
import Plan from "./pages/Plan";
import Monitoring from "./pages/Monitoring";
import SwitchDetail from "./pages/SwitchDetail";
import Discover from "./pages/add/Discover";
import ManualIP from "./pages/add/ManualIP";
import SerialAdd from "./pages/add/Serial";
import CatalogAdd from "./pages/add/Catalog";
import ResetProcedures from "./pages/add/ResetProcedures";
import ScanPrefixes from "./pages/add/ScanPrefixes";
import DiscoveryCredentials from "./pages/DiscoveryCredentials";
import Profils from "./pages/Profils";
import SiteConfigIO from "./pages/SiteConfigIO";
import ConfigDevices from "./pages/ConfigDevices";
import SpanningTreeConfig from "./pages/SpanningTreeConfig";
import MgmtPlanPage from "./pages/MgmtPlan";
import SiteCredentialsPage from "./pages/SiteCredentials";
import DebugPage from "./pages/Debug";
import PipelinesPage from "./pages/Pipelines";
import TagsPage from "./pages/Tags";
import Vlans from "./pages/Vlans";
import Profiles from "./pages/Profiles";
import SwitchProfiles from "./pages/SwitchProfiles";
import Users from "./pages/Users";
import Sites from "./pages/Sites";
import ProxyPage from "./pages/Proxy";
import { get, type Device, type Site } from "./api";
import { LAST_SITE_KEY, sitePath } from "./paths";
import { useSite } from "./useSite";

/** Map a legacy path suffix (after /s/:id/) onto the English tree. */
function modernizeSuffix(suffix: string): string {
  const map: Record<string, string> = {
    equipements: "equipment",
    parc: "equipment",
    plan: "topology",
    decouvertes: "discover",
    serie: "serial",
    catalogue: "catalog",
    reinitialiser: "reset",
    plages: "prefixes",
    identifiants: "credentials",
    etiquettes: "tags",
    "profils-ports": "port-profiles",
    "profils-equipements": "device-profiles",
    "profils-switchs": "switch-profiles",
    groupes: "switch-profiles",
  };
  const parts = suffix.split("/").filter(Boolean);
  if (parts[0] === "sw" && parts[1]) {
    // Device UUID → name resolved by LegacyDeviceRedirect.
    return "equipment/" + parts[1];
  }
  if (parts[0] === "add" && parts[1] && map[parts[1]]) {
    parts[1] = map[parts[1]];
  }
  if (parts[0] === "config" && parts[1]) {
    if (parts[1] === "equipements") parts[1] = "devices";
    else if (map[parts[1]]) parts[1] = map[parts[1]];
  }
  if (parts[0] && map[parts[0]] && parts[0] !== "config" && parts[0] !== "add") {
    parts[0] = map[parts[0]];
  }
  return parts.join("/");
}

/** Old /s/:siteId/… bookmarks → /{slug}/…. */
function LegacySiteRedirect() {
  const { siteId } = useParams();
  const star = useParams()["*"] ?? "";
  const { data: sites } = useQuery({
    queryKey: ["sites"],
    queryFn: () => get<Site[]>("/api/sites"),
  });
  if (!sites) return <div className="empty">…</div>;
  const site = sites.find((s) => s.id === siteId || s.slug === siteId);
  if (!site) return <Navigate to="/config/sites" replace />;
  const raw = star ?? "";
  if (raw.startsWith("sw/")) {
    return <LegacyDeviceRedirect site={site} deviceId={raw.slice(3).split("/")[0]} />;
  }
  const suffix = modernizeSuffix(raw);
  if (!suffix) return <Navigate to={sitePath(site.slug, "dashboard")} replace />;
  return <Navigate to={sitePath(site.slug, ...suffix.split("/").filter(Boolean))} replace />;
}

function LegacyDeviceRedirect({ site, deviceId }: { site: Site; deviceId: string }) {
  const { data: devices } = useQuery({
    queryKey: ["devices", site.id],
    queryFn: () => get<Device[]>(`/api/sites/${site.id}/devices`),
  });
  if (!devices) return <div className="empty">…</div>;
  const d = devices.find((x) => x.id === deviceId);
  if (!d) return <Navigate to={sitePath(site.slug, "equipment")} replace />;
  return <Navigate to={sitePath(site.slug, "equipment", d.name)} replace />;
}

function RedirectToEquipment() {
  const { site } = useSite();
  if (!site) return <Navigate to="/config/sites" replace />;
  return <Navigate to={sitePath(site.slug, "equipment")} replace />;
}

function RedirectToSwitchProfiles() {
  const { site } = useSite();
  if (!site) return <Navigate to="/config/sites" replace />;
  return <Navigate to={sitePath(site.slug, "config", "switch-profiles")} replace />;
}

function RedirectHome() {
  const { site } = useSite();
  if (site) return <Navigate to={sitePath(site.slug, "dashboard")} replace />;
  const last = localStorage.getItem(LAST_SITE_KEY);
  return <Navigate to={last ? sitePath(last, "dashboard") : "/config/sites"} replace />;
}

function RedirectToConfigPipelines() {
  const { siteSlug } = useParams();
  if (!siteSlug) return <Navigate to="/config/sites" replace />;
  return <Navigate to={sitePath(siteSlug, "config", "pipelines")} replace />;
}

export default function App() {
  const { t } = useTranslation();
  const { user, loading } = useAuth();
  if (loading) return <div className="empty">{t("common.loading")}</div>;
  if (!user) return <Login />;

  return (
    <Routes>
      <Route element={<Layout />}>
        <Route path="/" element={<Navigate to="/config/sites" replace />} />

        {/* Day-to-day — /{siteSlug}/… */}
        <Route path="/:siteSlug/dashboard" element={<Dashboard />} />
        <Route path="/:siteSlug/equipment" element={<Equipements />} />
        <Route path="/:siteSlug/equipment/:deviceName" element={<SwitchDetail />} />
        <Route path="/:siteSlug/ports" element={<Ports />} />
        <Route path="/:siteSlug/topology" element={<Plan />} />
        <Route path="/:siteSlug/monitoring" element={<Monitoring />} />

        {/* Add equipment */}
        <Route path="/:siteSlug/add" element={<Navigate to="discover" replace />} />
        <Route path="/:siteSlug/add/discover" element={<Discover />} />
        <Route path="/:siteSlug/add/ip" element={<ManualIP />} />
        <Route path="/:siteSlug/add/serial" element={<SerialAdd />} />
        <Route path="/:siteSlug/add/catalog" element={<CatalogAdd />} />
        <Route path="/:siteSlug/add/reset" element={<ResetProcedures />} />
        <Route path="/:siteSlug/add/prefixes" element={<ScanPrefixes />} />

        {/* Configuration */}
        <Route path="/:siteSlug/config/vlans" element={<Vlans />} />
        <Route path="/:siteSlug/config/management" element={<MgmtPlanPage />} />
        <Route path="/:siteSlug/config/spanning-tree" element={<SpanningTreeConfig />} />
        <Route path="/:siteSlug/config/credentials" element={<SiteCredentialsPage />} />
        <Route path="/:siteSlug/config/tags" element={<TagsPage />} />
        <Route path="/config/debug" element={<DebugPage />} />
        <Route path="/:siteSlug/config/port-profiles" element={<Profils />} />
        <Route
          path="/:siteSlug/config/device-profiles"
          element={<Profiles kind="device" />}
        />
        <Route path="/:siteSlug/config/switch-profiles" element={<SwitchProfiles />} />
        <Route
          path="/:siteSlug/config/groupes"
          element={<RedirectToSwitchProfiles />}
        />
        <Route path="/:siteSlug/config/pipelines" element={<PipelinesPage />} />
        <Route path="/:siteSlug/pipelines" element={<RedirectToConfigPipelines />} />
        <Route path="/:siteSlug/config/devices" element={<ConfigDevices />} />
        <Route path="/:siteSlug/config/import-export" element={<SiteConfigIO />} />
        <Route path="/config/sites" element={<Sites />} />
        <Route path="/config/users" element={<Users />} />
        <Route path="/config/proxy" element={<ProxyPage />} />
        <Route path="/config/identifiants" element={<DiscoveryCredentials />} />
        <Route path="/config/credentials" element={<DiscoveryCredentials />} />

        {/* Legacy /s/:uuid/… */}
        <Route path="/s/:siteId/*" element={<LegacySiteRedirect />} />
        <Route path="/s/:siteId" element={<LegacySiteRedirect />} />
        <Route path="/sites" element={<Navigate to="/config/sites" replace />} />
        <Route
          path="/:siteSlug/add/identifiants"
          element={<Navigate to="/config/credentials" replace />}
        />
        <Route path="/:siteSlug/parc" element={<RedirectToEquipment />} />
        <Route path="*" element={<RedirectHome />} />
      </Route>
    </Routes>
  );
}
