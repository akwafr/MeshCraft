import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { ActionButton, ErrorMsg, Field } from "../components";
import LanguageSelector from "../i18n/LanguageSelector";
import LoginBackdrop from "../LoginBackdrop";
import Logo from "../Logo";
import { Form } from "../ui/Form";

export default function Login() {
  const { t } = useTranslation();
  const { login } = useAuth();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);

  const submit = async () => {
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
      <LoginBackdrop />
      <Form className="card login-card" onSubmit={submit} busy={busy}>
        {/* On the login screen, the name stays written next to the brand:
            this is the first contact, ambiguity is not desirable here. */}
        <div className="login-brand">
          <Logo size={40} />
          <h1>
            mesh<span className="login-brand-accent">craft</span>
          </h1>
        </div>
        <Field label={t("login.email")}>
          <input
            type="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            autoComplete="username"
            autoFocus
            required
          />
        </Field>
        <Field label={t("login.password")}>
          <input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete="current-password"
            required
          />
        </Field>
        <ErrorMsg error={error} />
        <ActionButton kind="connect" type="submit" className="login-submit" disabled={busy}>
          {busy ? t("login.connecting") : t("login.submit")}
        </ActionButton>
        <div className="login-lang">
          <LanguageSelector />
        </div>
      </Form>
    </div>
  );
}
