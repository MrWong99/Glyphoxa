import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { RouterProvider } from "@tanstack/react-router";

import { Providers } from "@/app/Providers";
import { router } from "@/app/router";
import { detectLang, loadCatalog } from "@/i18n";
import { transport } from "@/lib/transport";
import { makeQueryClient } from "@/lib/queryClient";

import "@/styles/fonts.css";
import "@/styles/tokens.css";
import "@/styles/base.css";
import "@/styles/components.css";

const queryClient = makeQueryClient();

const rootEl = document.getElementById("root");
if (!rootEl) {
  throw new Error("missing #root element");
}
const root = createRoot(rootEl);

// The display language's catalog is fetched BEFORE the first render (English
// is in the bundle; anything else is its own chunk), so the app paints once,
// in the right language. A failed fetch must not keep the app from starting:
// it renders on the English fallback and the provider retries.
void loadCatalog(detectLang())
  .catch(() => {})
  .then(() =>
    root.render(
      <StrictMode>
        <Providers transport={transport} queryClient={queryClient}>
          <RouterProvider router={router} />
        </Providers>
      </StrictMode>,
    ),
  );
