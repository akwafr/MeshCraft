import React from "react";
import ReactDOM from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { BrowserRouter } from "react-router-dom";
import App from "./App";
import { AuthProvider } from "./auth";
import "./i18n";
import "./styles.css";
// AFTER styles.css, and that order is load-bearing: tokens.css is the
// palette and must win over anything the sheet declared before it.
import "./ui/tokens.css";
import { applyStored } from "./ui/theme";

// Before the first render: re-applies the attribute the inline script in
// index.html already set, and sets the <meta name="theme-color"> that the
// inline script deliberately leaves alone.
applyStored();

const queryClient = new QueryClient({
  defaultOptions: { queries: { retry: 1, staleTime: 10_000 } },
});

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <QueryClientProvider client={queryClient}>
      <AuthProvider>
        <BrowserRouter>
          <App />
        </BrowserRouter>
      </AuthProvider>
    </QueryClientProvider>
  </React.StrictMode>,
);
