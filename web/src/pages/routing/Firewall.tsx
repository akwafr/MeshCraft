import { useTranslation } from "react-i18next";
import { PageHeader } from "../../ui/PageHeader";
import { ComingSoon } from "./Placeholder";

export default function Firewall() {
  const { t } = useTranslation();
  return (
    <>
      <PageHeader title={t("firewallPage.title")} />
      <ComingSoon blurb={t("firewallPage.blurb")} />
    </>
  );
}
