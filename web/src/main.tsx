import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { RouterProvider } from "@tanstack/react-router";

import { Providers } from "@/app/Providers";
import { router } from "@/app/router";
import { transport } from "@/lib/transport";
import { makeQueryClient } from "@/lib/queryClient";

import "@/styles/tokens.css";
import "@/styles/base.css";
import "@/styles/components.css";

const queryClient = makeQueryClient();

const rootEl = document.getElementById("root");
if (!rootEl) {
  throw new Error("missing #root element");
}

createRoot(rootEl).render(
  <StrictMode>
    <Providers transport={transport} queryClient={queryClient}>
      <RouterProvider router={router} />
    </Providers>
  </StrictMode>,
);
