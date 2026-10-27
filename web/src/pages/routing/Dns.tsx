import { useTranslation } from "react-i18next";
import { PageHeader } from "../../ui/PageHeader";
import { ComingSoon } from "./Placeholder";

export default function Dns() {
  const { t } = useTranslation();
  return (
    <>
      <PageHeader title={t("dnsPage.title")} />
      <ComingSoon blurb={t("dnsPage.blurb")} />
    </>
  );
}
