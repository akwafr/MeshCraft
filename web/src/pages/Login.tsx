import { useState, type FormEvent } from "react";
import { useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { ErrorMsg } from "../components";
import LanguageSelector from "../i18n/LanguageSelector";
import Logo from "../Logo";

export default function Login() {
  const { t } = useTranslation();
  const { login } = useAuth();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await login(email, password);
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="login-wrap">
      <form className="card login-card" onSubmit={submit}>
        {/* On the login screen, the name stays written next to the brand:
            this is the first contact, ambiguity is not desirable here. */}
        <div className="login-brand">
          <Logo size={40} />
          <h1>
            mesh<span style={{ color: "var(--accent)" }}>craft</span>
          </h1>
        </div>
        <label>
          <span className="lbl">{t("login.email")}</span>
          <input
            type="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            autoFocus
            required
          />
        </label>
        <label>
          <span className="lbl">{t("login.password")}</span>
          <input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            required
          />
        </label>
        <ErrorMsg error={error} />
        <button type="submit" disabled={busy} style={{ width: "100%" }}>
          {busy ? t("login.connecting") : t("login.submit")}
        </button>
        <div className="login-lang">
          <LanguageSelector />
        </div>
      </form>
    </div>
  );
}
