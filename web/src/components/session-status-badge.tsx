import { Badge } from "@/components/ui/badge";
import type { Session } from "@/lib/types";

export function SessionStatusBadge({ status }: { status: Session["status"] }) {
  return (
    <Badge
      variant={
        status === "active"
          ? "default"
          : status === "ended"
            ? "secondary"
            : "destructive"
      }
      className={status === "active" ? "bg-green-600 hover:bg-green-600" : undefined}
    >
      {status === "active" && (
        <span className="mr-1.5 h-1.5 w-1.5 rounded-full bg-white animate-pulse" />
      )}
      {status}
    </Badge>
  );
}
