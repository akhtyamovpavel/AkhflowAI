import { useEffect, useRef, useState } from "react";
import { Bell } from "lucide-react";
import { Button } from "@/components/ui/button";
import { deadlineMessage, reminderKey, upcomingDeadlines } from "@/lib/deadlines";
import type { DeadlineTask } from "@/lib/deadlines";

type Props = {
  userId: string | null;
  project: string;
  fetchTasks: (signal: AbortSignal) => Promise<DeadlineTask[]>;
  onOpen: (id: number) => void;
};

export function DeadlineReminders({ userId, project, fetchTasks, onOpen }: Props) {
  const supported = "Notification" in window && window.isSecureContext;
  const [permission, setPermission] = useState<NotificationPermission>(
    supported ? Notification.permission : "denied",
  );
  const [tasks, setTasks] = useState<DeadlineTask[]>([]);
  const [error, setError] = useState("");
  const sent = useRef(new Set<string>());
  const notifications = useRef<Notification[]>([]);

  useEffect(() => {
    const active = notifications.current;
    return () => {
      for (const notification of active) {
        notification.close();
      }
    };
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    let loading = false;
    const refresh = async () => {
      if (loading || controller.signal.aborted) {
        return;
      }
      loading = true;
      try {
        const data = await fetchTasks(controller.signal);
        if (!controller.signal.aborted) {
          setTasks(upcomingDeadlines(data));
          setError("");
        }
      } catch {
        if (!controller.signal.aborted) {
          setTasks([]);
          setError("Не удалось проверить дедлайны. Повторим через минуту.");
        }
      } finally {
        loading = false;
      }
    };
    const onFocus = () => {
      if (supported) {
        setPermission(Notification.permission);
      }
      void refresh();
    };
    void refresh();
    const timer = window.setInterval(refresh, 60_000);
    window.addEventListener("focus", onFocus);
    document.addEventListener("visibilitychange", onFocus);
    return () => {
      controller.abort();
      window.clearInterval(timer);
      window.removeEventListener("focus", onFocus);
      document.removeEventListener("visibilitychange", onFocus);
    };
  }, [fetchTasks, supported]);

  useEffect(() => {
    if (!userId || !supported || permission !== "granted" || Notification.permission !== "granted") {
      return;
    }
    for (const task of upcomingDeadlines(tasks)) {
      const key = reminderKey(userId, project, task);
      let delivered = sent.current.has(key);
      try {
        delivered = delivered || localStorage.getItem(key) === "sent";
      } catch {
        // Keep deduplication in memory when storage is unavailable.
      }
      if (delivered) {
        continue;
      }
      try {
        const notification = new Notification(`Akhflow: ${deadlineMessage(task)}`, {
          body: `#${task.id} ${task.title}`,
          tag: key,
        });
        notifications.current.push(notification);
        notification.onclick = () => {
          window.focus();
          onOpen(task.id);
          notification.close();
        };
        sent.current.add(key);
        try {
          localStorage.setItem(key, "sent");
        } catch {
          // The in-memory marker still prevents repeats in this tab.
        }
      } catch {
        setError("Браузер не смог показать уведомление. Напоминания доступны в интерфейсе.");
        break;
      }
    }
  }, [tasks, permission, userId, project, onOpen, supported]);

  const enable = async () => {
    try {
      setPermission(await Notification.requestPermission());
    } catch {
      setError("Не удалось включить уведомления браузера.");
    }
  };

  return (
    <section aria-label="Напоминания о дедлайнах" className="rounded-lg border bg-card p-4 space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="flex items-center gap-2 text-sm font-medium">
          <Bell className="h-4 w-4" aria-hidden="true" /> Ближайшие дедлайны
        </h2>
        {supported && permission === "default" && (
          <Button variant="outline" size="sm" onClick={enable}>Включить уведомления браузера</Button>
        )}
        {supported && permission === "granted" && (
          <span className="text-xs text-muted-foreground">Уведомления браузера включены</span>
        )}
      </div>
      <p className="text-xs text-muted-foreground">
        Напоминаем за день до срока, пока проект открыт в браузере.
        {!supported && " Уведомления браузера недоступны. Нужен поддерживаемый браузер и HTTPS или localhost."}
        {supported && permission === "denied" && " Для уведомлений браузера нужно разрешение в настройках сайта."}
      </p>
      <div aria-live="polite">
        {error && <p className="text-sm text-destructive">{error}</p>}
        {!error && tasks.length === 0 && <p className="text-sm text-muted-foreground">Нет задач со сроком сегодня или завтра.</p>}
        <ul className="space-y-1">
          {tasks.map((task) => (
            <li key={task.id}>
              <button className="w-full rounded-md px-2 py-2 text-left text-sm hover:bg-muted focus-visible:outline-2 focus-visible:outline-ring" onClick={() => onOpen(task.id)}>
                <span className="font-medium">{deadlineMessage(task)}:</span> #{task.id} {task.title}
              </button>
            </li>
          ))}
        </ul>
      </div>
    </section>
  );
}
