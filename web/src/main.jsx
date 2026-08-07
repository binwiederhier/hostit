import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import App from "./App";
import { applyTheme, getTheme } from "./theme";
import "./styles.css";

// Apply the saved theme before the first paint, so there is no light-mode flash.
applyTheme(getTheme());

createRoot(document.getElementById("root")).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
