// Styled "coming soon" placeholder for the Campaign and Session screens. Those
// screens wire to their RPCs in later stages (#67+); this stage ships only the
// Configuration screen on the live GetActiveCampaign RPC (ADR-0039).
export function Placeholder({ title }: { title: string }) {
  return (
    <>
      <header className="page-header">
        <div>
          <h1 className="page-title">{title}</h1>
          <p className="page-subtitle">This screen lands in a later stage.</p>
        </div>
        <span className="coming-soon">coming soon</span>
      </header>
      <section className="card" data-disabled="true">
        <p className="card-desc">Nothing here yet.</p>
      </section>
    </>
  );
}
