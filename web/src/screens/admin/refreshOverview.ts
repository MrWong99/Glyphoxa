import { useQueryClient } from "@tanstack/react-query";

import { AdminService } from "@gen/glyphoxa/management/v1/management_pb";
import { invalidateMethodQueries } from "@/lib/queryClient";

/**
 * Re-reads the Admin screen's one read after a write; the promise resolves
 * once the fresh overview is in, so a caller can hold its pending state until
 * the list on screen is the server's again.
 */
export function useRefreshOverview(): () => Promise<void> {
  const queryClient = useQueryClient();
  return () => invalidateMethodQueries(queryClient, AdminService.method.getAccessOverview);
}
