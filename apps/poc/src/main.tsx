import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./app/App";
import "./styles.css";
import { applyTheme, savedTheme, themeCss } from "./theme/css";

const style = document.createElement("style");
style.textContent = themeCss();
document.head.prepend(style);
applyTheme(savedTheme());

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
