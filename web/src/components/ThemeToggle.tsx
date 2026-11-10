import { useEffect, useState } from "react";
import { Moon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { initializeTheme, saveTheme } from "@/lib/theme";

export function ThemeToggle() {
  const [dark, setDark] = useState(() => document.documentElement.classList.contains("dark"));

  useEffect(() => {
    const syncTheme = (event: StorageEvent) => {
      if (event.key !== "akhflow.theme" && event.key !== null) {
        return;
      }
      initializeTheme();
      setDark(document.documentElement.classList.contains("dark"));
    };
    window.addEventListener("storage", syncTheme);
    return () => window.removeEventListener("storage", syncTheme);
  }, []);

  const toggle = () => {
    const nextDark = !dark;
    saveTheme(nextDark);
    setDark(nextDark);
  };

  return (
    <Button type="button" variant="outline" size="sm" aria-pressed={dark} onClick={toggle}>
      <Moon aria-hidden="true" /> Тёмная тема
    </Button>
  );
}
