import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import {
  del,
  get,
  patch,
  post,
  put,
  type Role,
  type Site,
  type User,
} from "../api";
import { useAuth } from "../auth";
import { ActionButton, DeleteButton, ErrorMsg, Field, Modal } from "../components";

const emptyForm = {
  email: "",
  display_name: "",
  password: "",
  is_global_admin: false,
};

export default function Users() {
  const { t } = useTranslation();
  const { user: me } = useAuth();
  const qc = useQueryClient();
  const [editing, setEditing] = useState<User | "new" | null>(null);
  const [form, setForm] = useState(emptyForm);
  const [roles, setRoles] = useState<Role[]>([]);

  const { data: users, error } = useQuery({
    queryKey: ["users"],
    queryFn: () => get<User[]>("/api/users"),
  });
  const { data: sites } = useQuery({
    queryKey: ["sites"],
    queryFn: () => get<Site[]>("/api/sites"),
  });

  const invalidate = () => qc.invalidateQueries({ queryKey: ["users"] });

  const save = useMutation({
    mutationFn: async () => {
      let u: User;
      if (editing === "new") {
        u = await post<User>("/api/users", form);
      } else {
        u = await patch<User>(`/api/users/${(editing as User).id}`, form);
      }
      if (!form.is_global_admin || roles.length > 0) {
        await put(`/api/users/${u.id}/roles`, roles);
      }
      return u;
    },
    onSuccess: () => {
      invalidate();
      setEditing(null);
    },
  });

  const remove = useMutation({
    mutationFn: (id: string) => del(`/api/users/${id}`),
    onSuccess: invalidate,
  });

  const open = (u: User | "new") => {
    setEditing(u);
    save.reset();
    if (u === "new") {
      setForm(emptyForm);
      setRoles([]);
    } else {
      setForm({
        email: u.email,
        display_name: u.display_name,
        password: "",
        is_global_admin: u.is_global_admin,
      });
      setRoles(u.roles ?? []);
    }
  };

  const setRole = (i: number, patch: Partial<Role>) =>
    setRoles(roles.map((r, j) => (i === j ? { ...r, ...patch } : r)));

  return (
    <>
      <div className="toolbar">
        <h1>{t("usersPage.title")}</h1>
        <ActionButton kind="create" onClick={() => open("new")}>{t("usersPage.createUser")}</ActionButton>
      </div>
      <ErrorMsg error={error} />
      <div className="card">
        <table>
          <thead>
            <tr>
              <th>Email</th>
              <th>{t("common.name")}</th>
              <th>{t("usersPage.accessCol")}</th>
              <th style={{ width: 200 }}></th>
            </tr>
          </thead>
          <tbody>
            {(users ?? []).map((u) => (
              <tr key={u.id}>
                <td>{u.email}</td>
                <td>{u.display_name}</td>
                <td>
                  {u.is_global_admin ? (
                    <span className="badge admin">{t("common.globalAdmin")}</span>
                  ) : (
                    (u.roles ?? []).map((r) => (
                      <span key={r.site_id} className="badge" style={{ marginRight: 4 }}>
                        {r.site_name} : {r.role}
                      </span>
                    ))
                  )}
                </td>
                <td style={{ textAlign: "right" }}>
                  <ActionButton kind="edit" className="ghost" onClick={() => open(u)}>
                    {t("common.edit")}
                  </ActionButton>
                  {u.id !== me?.id && (
                    <DeleteButton onConfirm={() => remove.mutate(u.id)} />
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      {editing && (
        <Modal
          title={(editing === "new" ? t("usersPage.createUser") : t("usersPage.editUser", { email: form.email })) as string}
          onClose={() => setEditing(null)}
        >
          {editing === "new" && (
            <Field label="Email">
              <input
                type="email"
                value={form.email}
                onChange={(e) => setForm({ ...form, email: e.target.value })}
                autoFocus
              />
            </Field>
          )}
          <Field label={t("usersPage.displayName") as string}>
            <input
              value={form.display_name}
              onChange={(e) => setForm({ ...form, display_name: e.target.value })}
            />
          </Field>
          <Field
            label={
              (editing === "new"
                ? t("usersPage.passwordMin")
                : t("usersPage.newPasswordOptional")) as string
            }
          >
            <input
              type="password"
              value={form.password}
              onChange={(e) => setForm({ ...form, password: e.target.value })}
            />
          </Field>
          <label className="row">
            <input
              type="checkbox"
              checked={form.is_global_admin}
              disabled={editing !== "new" && (editing as User).id === me?.id}
              onChange={(e) =>
                setForm({ ...form, is_global_admin: e.target.checked })
              }
            />
            {t("usersPage.globalAdminCheckbox")}
          </label>

          {!form.is_global_admin && (
            <>
              <div className="lbl" style={{ margin: "12px 0 6px", color: "var(--text-dim)" }}>
                {t("usersPage.rolesPerSite")}
              </div>
              {roles.map((r, i) => (
                <div key={i} className="row" style={{ marginBottom: 8 }}>
                  <select
                    style={{ flex: 2 }}
                    value={r.site_id}
                    onChange={(e) => setRole(i, { site_id: e.target.value })}
                  >
                    <option value="">{t("usersPage.siteOption")}</option>
                    {(sites ?? []).map((s) => (
                      <option key={s.id} value={s.id}>
                        {s.name}
                      </option>
                    ))}
                  </select>
                  <select
                    style={{ flex: 1 }}
                    value={r.role}
                    onChange={(e) =>
                      setRole(i, { role: e.target.value as Role["role"] })
                    }
                  >
                    <option value="viewer">viewer</option>
                    <option value="operator">operator</option>
                    <option value="site_admin">site_admin</option>
                  </select>
                  <button
                    className="ghost"
                    onClick={() => setRoles(roles.filter((_, j) => j !== i))}
                  >
                    ✕
                  </button>
                </div>
              ))}
              <ActionButton
                kind="role"
                className="secondary"
                onClick={() =>
                  setRoles([...roles, { site_id: "", role: "viewer" }])
                }
              >
                {t("usersPage.addRole")}
              </ActionButton>
            </>
          )}

          <ErrorMsg error={save.error} />
          <div className="actions">
            <ActionButton kind="cancel" className="secondary" onClick={() => setEditing(null)}>
              {t("common.cancel")}
            </ActionButton>
            <ActionButton
              kind="save"
              disabled={
                save.isPending ||
                (editing === "new" && (!form.email || form.password.length < 8)) ||
                roles.some((r) => !r.site_id)
              }
              onClick={() => save.mutate()}
            >
              {t("common.save")}
            </ActionButton>
          </div>
        </Modal>
      )}
    </>
  );
}
