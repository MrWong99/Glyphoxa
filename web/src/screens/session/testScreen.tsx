import { MemoryScreenNav, type ScreenLocation, type ScreenSearch } from "@/app/screenNav";
import { Session } from "./Session";

// Test-only: the Session screen under an in-memory navigation seam. The screen
// keeps "which session is on screen" in the URL (?session=, through
// useScreenNav), so a test that picks a session, follows a search hit or
// arrives by deep link needs a ScreenNav that actually moves — outside a
// provider the seam is inert and those clicks would do nothing.
//
//   render(<Providers …><Screen search={{ session: "vsOld", line: "a:t1" }} onNavigate={spy} /></Providers>)
export function Screen({
  search,
  onNavigate,
}: {
  search?: ScreenSearch;
  onNavigate?: (next: ScreenLocation, opts: { replace: boolean }) => void;
}) {
  return (
    <MemoryScreenNav initial={{ screen: "session", search }} onNavigate={onNavigate}>
      <Session />
    </MemoryScreenNav>
  );
}
