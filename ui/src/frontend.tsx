// The page's entry point: renders App into #root.
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";

const app = (
  <StrictMode>
    <App />
  </StrictMode>
);

// Keep the root across hot reloads (bun dev).
(import.meta.hot.data.root ??= createRoot(document.getElementById("root")!)).render(app);
