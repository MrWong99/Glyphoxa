"use client";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState, useEffect, type ReactNode } from "react";
import { usePathname } from "next/navigation";
import { TooltipProvider } from "@/components/ui/tooltip";
import { Toaster, toast } from "sonner";

/** Dismiss all toasts on client-side route changes. */
function RouteChangeToastDismisser() {
  const pathname = usePathname();
  useEffect(() => {
    toast.dismiss();
  }, [pathname]);
  return null;
}

export function Providers({ children }: { children: ReactNode }) {
  const [queryClient] = useState(
    () =>
      new QueryClient({
        defaultOptions: {
          queries: {
            staleTime: 30_000,
            retry: 1,
          },
        },
      }),
  );

  return (
    <QueryClientProvider client={queryClient}>
      <TooltipProvider>
        <RouteChangeToastDismisser />
        {children}
        <Toaster
          theme="dark"
          position="bottom-right"
          richColors
          closeButton
          duration={4000}
        />
      </TooltipProvider>
    </QueryClientProvider>
  );
}
