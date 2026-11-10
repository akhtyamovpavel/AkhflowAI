const themeKey = "akhflow.theme";

export function applyTheme(dark: boolean) {
  document.documentElement.classList.toggle("dark", dark);
}

export function initializeTheme() {
  let preference: string | null = null;
  try {
    preference = localStorage.getItem(themeKey);
  } catch {
    preference = null;
  }
  let dark = preference === "dark";
  if (preference !== "dark" && preference !== "light") {
    dark = window.matchMedia("(prefers-color-scheme: dark)").matches;
  }
  applyTheme(dark);
}

export function saveTheme(dark: boolean) {
  applyTheme(dark);
  try {
    localStorage.setItem(themeKey, dark ? "dark" : "light");
  } catch {
    // The theme still applies for this page if storage is unavailable.
  }
}
