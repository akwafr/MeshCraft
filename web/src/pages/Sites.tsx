import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { sitePath } from "../paths";
import { del, get, patch, post, type Site } from "../api";
import { useAuth } from "../auth";
import { ActionButton, DeleteButton, ErrorMsg, Field, Modal } from "../components";

export default function Sites() {
  const { t, i18n } = useTranslation();
  const { user } = useAuth();
  const qc = useQueryClient();
  const [editing, setEditing] = useState<Site | "new" | null>(null);
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");

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
                <th style={{ width: 220 }}></th>
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
                    {new Date(s.created_at).toLocaleDateString(i18n.language)}
                  </td>
                  <td style={{ textAlign: "right" }}>
                    <ActionButton kind="rename" className="ghost" onClick={() => open(s)}>
                      {t("common.rename")}
                    </ActionButton>
                    {user?.is_global_admin && (
                      <DeleteButton onConfirm={() => remove.mutate(s.id)} />
                    )}
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
