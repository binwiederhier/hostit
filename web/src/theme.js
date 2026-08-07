// Theme preference: "system" (follow the OS via prefers-color-scheme), "light",
// or "dark". An explicit choice stamps data-theme on the root so styles.css
// overrides the media query; "system" leaves it unset so the OS wins.

const KEY = "hostit.theme";
export const THEMES = ["system", "light", "dark"];

export const getTheme = () => {
  const t = localStorage.getItem(KEY);
  return t === "light" || t === "dark" ? t : "system";
};

export const applyTheme = (theme) => {
  const root = document.documentElement;
  if (theme === "light" || theme === "dark") {
    root.setAttribute("data-theme", theme);
  } else {
    root.removeAttribute("data-theme");
  }
};

export const setTheme = (theme) => {
  if (theme === "light" || theme === "dark") {
    localStorage.setItem(KEY, theme);
  } else {
    localStorage.removeItem(KEY);
  }
  applyTheme(theme);
};
