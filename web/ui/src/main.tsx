import "@mantine/core/styles.css";

import { MantineProvider } from "@mantine/core";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryParamProvider } from "use-query-params";
import { WindowHistoryAdapter } from "use-query-params/adapters/window";

import { App } from "./App";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // Target state changes on the poll interval, so briefly reusing a
      // result avoids refetching the same page on every render.
      staleTime: 5000,
      retry: 1,
      refetchOnWindowFocus: false,
    },
  },
});

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <MantineProvider defaultColorScheme="auto">
      <QueryClientProvider client={queryClient}>
        <QueryParamProvider adapter={WindowHistoryAdapter}>
          <App />
        </QueryParamProvider>
      </QueryClientProvider>
    </MantineProvider>
  </StrictMode>,
);
