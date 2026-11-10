import { useEffect, useState } from "react";
import { Button } from "./ui/button";

type EmailSettings = { enabled: boolean; available: boolean };
type Request = (path: string, options?: RequestInit, token?: string | null) => Promise<any>;

export function EmailNotifications({ token, request }: { token: string; request: Request }) {
  const [settings, setSettings] = useState<EmailSettings | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    let active = true;
    request("/api/settings/email-notifications", {}, token)
      .then((data: EmailSettings) => { if (active) setSettings(data); })
      .catch(() => { if (active) setError("Не удалось загрузить настройки email"); });
    return () => { active = false; };
  }, [token, request]);

  const toggle = async () => {
    if (!settings || busy) return;
    setBusy(true);
    setError("");
    try {
      const data = await request("/api/settings/email-notifications", {
        method: "PUT",
        body: JSON.stringify({ enabled: !settings.enabled }),
      }, token);
      setSettings(data);
    } catch {
      setError("Не удалось сохранить настройки email");
    } finally {
      setBusy(false);
    }
  };

  const unavailable = settings && !settings.available && !settings.enabled;
  return (
    <span className="flex items-center gap-2">
      {error && <span role="alert" className="text-xs text-destructive">{error}</span>}
      <Button variant="ghost" size="sm" onClick={toggle} disabled={!settings || busy || !!unavailable}
        aria-pressed={settings?.enabled ?? false}
        title={unavailable ? "Отправка email не настроена на сервере" : "Уведомления о статусе назначенных задач"}>
        Email: {settings?.enabled ? "вкл" : "выкл"}
      </Button>
    </span>
  );
}
