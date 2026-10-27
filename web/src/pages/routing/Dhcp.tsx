import { useTranslation } from "react-i18next";
import { PageHeader } from "../../ui/PageHeader";
import { ComingSoon } from "./Placeholder";

export default function Dhcp() {
  const { t } = useTranslation();
  return (
    <>
      <PageHeader title={t("dhcpPage.title")} />
      <ComingSoon blurb={t("dhcpPage.blurb")} />
    </>
  );
}
