/**
 * The routing bar's four screens exist before their data does — ADR-0013
 * made them navigation entries, and an entry with no page behind it is a
 * 404 waiting for the first person who follows a link.
 *
 * SO THEY ARE HONEST SHELLS. A title and one sentence saying what the page
 * will serve. No placeholder chart, no table of invented rows, no disabled
 * button hinting at a gesture that does not exist: a mock that looks like it
 * works is read as a feature that is broken, and it costs more credibility
 * than an empty page costs patience. The model is being written now, by
 * other hands; what lands here is the shell and the route.
 *
 * Only the sentence is shared. The `PageHeader` stays at each call site, as
 * on every other page, so the title is a `t()` call a translator can grep in
 * the page that owns it.
 */

export function ComingSoon({
  blurb,
}: {
  /** One sentence. What the page will show, in the present of the product. */
  blurb: string;
}) {
  return (
    <div className="card">
      <p className="soon-blurb">{blurb}</p>
    </div>
  );
}
