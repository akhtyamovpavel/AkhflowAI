import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";

type Run = {
  id: number;
  chat_id: number;
  status: string;
  cancel_requested: boolean;
  step: { iteration: number; max_iterations: number; tool?: string };
  steps: { sequence: number; summary: string }[];
};

type Request = (path: string, options?: RequestInit, token?: string | null, expectedStatus?: number) => Promise<any>;

const labels: Record<string, string> = {
  queued: "В очереди",
  running: "Агент работает",
  succeeded: "Выполнено",
  step_limit: "Достигнут предел шагов",
  failed: "Не удалось выполнить",
  canceled: "Отменено",
  interrupted: "Выполнение прервано",
};

export function AgentRuns({ chatId, token, request, refresh, revision }: {
  chatId: number;
  token: string;
  request: Request;
  refresh: (chatId: number) => Promise<boolean>;
  revision: number;
}) {
  const [runs, setRuns] = useState<Run[]>([]);
  const [error, setError] = useState(false);
  const [canceling, setCanceling] = useState<number | null>(null);
  const [reload, setReload] = useState(0);

  useEffect(() => {
    let stopped = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const controller = new AbortController();
    let previous = new Map<number, string>();
    setRuns([]);
    setError(false);
    const poll = async () => {
      try {
        const data: unknown = await request(`/api/chats/${chatId}/runs`, { signal: controller.signal }, token, 200);
        if (!Array.isArray(data)) throw new Error("invalid runs");
        if (stopped) return;
        const next = data as Run[];
        const firstLoad = previous.size === 0;
        const completed = next.some((run) => {
          const was = previous.get(run.id);
          return (was === "queued" || was === "running") && run.status !== "queued" && run.status !== "running";
        });
        previous = new Map(next.map((run) => [run.id, run.status]));
        setRuns(next);
        setError(false);
        if (completed || (firstLoad && next.some((run) => run.status === "succeeded"))) void refresh(chatId);
      } catch {
        if (!stopped) setError(true);
      }
      if (!stopped) timer = setTimeout(poll, 3000);
    };
    void poll();
    return () => {
      stopped = true;
      controller.abort();
      if (timer) clearTimeout(timer);
    };
  }, [chatId, token, request, refresh, revision, reload]);

  const cancel = async (id: number) => {
    setCanceling(id);
    try {
      await request(`/api/runs/${id}/cancel`, { method: "POST", body: "{}" }, token, 202);
      setReload((value) => value + 1);
    } catch {
      setError(true);
    } finally {
      setCanceling(null);
    }
  };

  return (
    <div className="space-y-2" aria-live="polite">
      {error && <p className="text-sm text-destructive">Не удалось обновить состояние агента. Повторяем попытку.</p>}
      {runs.map((run) => (
        <div key={run.id} className="rounded-md border p-2 text-sm">
          <span>{labels[run.status] ?? "Состояние неизвестно"}</span>
          {run.status === "running" && run.step && <span> · шаг {run.step.iteration} из {run.step.max_iterations}</span>}
          {run.steps?.map((step) => step.summary && <p key={step.sequence} className="text-muted-foreground">{step.summary}</p>)}
          {(run.status === "queued" || run.status === "running") && (
            <Button size="sm" variant="outline" className="ml-2" disabled={canceling === run.id || run.cancel_requested} onClick={() => void cancel(run.id)}>
              {run.cancel_requested ? "Отмена запрошена" : "Остановить"}
            </Button>
          )}
        </div>
      ))}
    </div>
  );
}
