import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { viewerDate } from "../ui/clock";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { sitePath } from "../paths";
import { del, get, patch, post, type Site } from "../api";
import { useAuth } from "../auth";
import { ActionButton, ErrorMsg, Field, Modal } from "../components";
import { IconAction, IconDeleteButton } from "../ui/IconAction";

export default function Sites() {
  const { t, i18n } = useTranslation();
  const { user } = useAuth();
  const qc = useQueryClient();
  const [editing, setEditing] = useState<Site | "new" | null>(null);
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  // The site's local DNS domain. It used to be a constant compiled into the
  // Cisco connector ("meshcraft.local") and pushed onto every switch of every
  // site — and it is not an internal detail: the device carries it in its own
  // FQDN afterwards, which the lab's LLDP neighbours show verbatim
  // ("CiscoLabFirst1.meshcraft.local"). A site that already has a domain was
  // getting equipment announcing one that resolves nowhere.
  const [domain, setDomain] = useState("");

  const { data: sites, error } = useQuery({
    queryKey: ["sites"],
    queryFn: () => get<Site[]>("/api/sites"),
  });

  const invalidate = () => qc.invalidateQueries({ queryKey: ["sites"] });

  const save = useMutation({
    mutationFn: () =>
      editing === "new"
        ? post<Site>("/api/sites", { name })
        : patch<Site>(`/api/sites/${(editing as Site).id}`, {
            name,
            slug: slug.trim() || undefined,
            // ALWAYS SENT, including empty. The API reads this field as a
            // pointer so it carries three states — absent leaves the domain
            // alone, a value sets it, "" means "this site has no domain" — and
            // omitting it on a rename would make the last state unreachable
            // from this form.
            local_domain: domain.trim(),
          }),
    onSuccess: () => {
      invalidate();
      setEditing(null);
    },
  });

  const remove = useMutation({
    mutationFn: (id: string) => del(`/api/sites/${id}`),
    onSuccess: invalidate,
  });

  const open = (s: Site | "new") => {
    setEditing(s);
    setName(s === "new" ? "" : s.name);
    setSlug(s === "new" ? "" : s.slug);
    setDomain(s === "new" ? "" : (s.local_domain ?? ""));
    save.reset();
  };

  return (
    <>
      <div className="toolbar">
        <h1>{t("sites.title")}</h1>
        {user?.is_global_admin && (
          <ActionButton kind="create" onClick={() => open("new")}>{t("sites.createSite")}</ActionButton>
        )}
      </div>
      <ErrorMsg error={error} />
      <div className="card">
        {(sites ?? []).length === 0 ? (
          <div className="empty">
            {t("sites.noSites")}{" "}
            {user?.is_global_admin
              ? t("sites.createFirst")
              : t("sites.askAccess")}
          </div>
        ) : (
          <table>
            <thead>
              <tr>
                <th>{t("common.name")}</th>
                <th>{t("sites.slugCol")}</th>
                <th>{t("sites.createdOn")}</th>
                <th className="col-actions"></th>
              </tr>
            </thead>
            <tbody>
              {(sites ?? []).map((s) => (
                <tr key={s.id}>
                  <td>
                    <Link to={sitePath(s.slug, "dashboard")}>{s.name}</Link>
                  </td>
                  <td className="mono note">{s.slug}</td>
                  <td className="note">
                    {(viewerDate(s.created_at) ?? new Date(s.created_at)).toLocaleDateString(i18n.language)}
                  </td>
                  <td className="col-actions">
                    <div className="icon-actions">
                      <IconAction
                        kind="rename"
                        label={t("common.rename") as string}
                        onClick={() => open(s)}
                      />
                      {user?.is_global_admin && (
                        <IconDeleteButton onConfirm={() => remove.mutate(s.id)} />
                      )}
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {editing && (
        <Modal
          title={(editing === "new" ? t("sites.createSite") : t("sites.renameSite")) as string}
          onClose={() => setEditing(null)}
        >
          <Field label={t("sites.siteName") as string}>
            <input value={name} onChange={(e) => setName(e.target.value)} autoFocus />
          </Field>
          {editing !== "new" && (
            <Field
              label={t("sites.slugLabel") as string}
              help={<p>{t("sites.slugHelp")}</p>}
            >
              <input
                className="mono"
                value={slug}
                onChange={(e) => setSlug(e.target.value.toLowerCase())}
                placeholder={t("sites.slugPlaceholder") as string}
              />
            </Field>
          )}
          {editing !== "new" && (
            <Field
              label={t("sites.domainLabel") as string}
              help={<p>{t("sites.domainHelp")}</p>}
            >
              <input
                className="mono"
                value={domain}
                onChange={(e) => setDomain(e.target.value.toLowerCase())}
                placeholder={t("sites.domainPlaceholder") as string}
              />
            </Field>
          )}
          {editing === "new" && (
            <p className="note">{t("sites.slugOnCreateNote")}</p>
          )}
          <ErrorMsg error={save.error} />
          <div className="actions">
            <ActionButton kind="cancel" className="secondary" onClick={() => setEditing(null)}>
              {t("common.cancel")}
            </ActionButton>
            <ActionButton kind="save" disabled={!name || save.isPending} onClick={() => save.mutate()}>
              {t("common.save")}
            </ActionButton>
          </div>
        </Modal>
      )}
    </>
  );
}
