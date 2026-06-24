import { Card, CardBody } from "@/components/ui/Card";
import { Badge } from "@/components/ui/Badge";

// Styled "coming soon" placeholder for the Campaign and Session screens. Those
// screens wire to their RPCs in later stages (#67+); this stage ships only the
// Configuration screen on the live GetActiveCampaign RPC (ADR-0039). Uses the
// arcane gx- vocabulary so it reads as part of the design, not a stub.
export function Placeholder({ title }: { title: string }) {
  return (
    <div className="gx-providers">
      <h1>{title}</h1>
      <p className="gx-providers__lede">This screen lands in a later stage.</p>
      <Card>
        <CardBody>
          <Badge variant="neutral" dot size="sm">
            coming soon
          </Badge>
        </CardBody>
      </Card>
    </div>
  );
}
