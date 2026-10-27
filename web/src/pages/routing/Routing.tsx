import { useTranslation } from "react-i18next";
import { PageHeader } from "../../ui/PageHeader";
import { ComingSoon } from "./Placeholder";

export default function Routing() {
  const { t } = useTranslation();
  return (
    <>
      <PageHeader title={t("routingPage.title")} />
      <ComingSoon blurb={t("routingPage.blurb")} />
    </>
  );
}
