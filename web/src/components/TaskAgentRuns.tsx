import { useEffect, useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import { isAgentRunList } from "@/lib/agentRuns";
import type { AgentRun } from "@/lib/agentRuns";
import { reminderUserId } from "@/lib/deadlines";

type Request = (path: string, options?: RequestInit, token?: string | null, expectedStatus?: number) => Promise<any>;
type Pending = { key: string; message: string };

const labels: Record<string, string> = {
  queued: "В очереди",
  running: "Агент работает",
  succeeded: "Выполнено",
  step_limit: "Достигнут предел шагов",
  failed: "Не удалось выполнить",
  canceled: "Отменено",
  interrupted: "Выполнение прервано",
};

export function TaskAgentRuns({ taskId, token, request, onChanged, onOpenChat }: {
  taskId: number;
  token: string;
  request: Request;
  onChanged: () => void | Promise<void>;
  onOpenChat: (chatId: number) => void;
}) {
  const actorId = reminderUserId(token);
  const storageKey = `akhflow.task-run.${actorId}.${taskId}`;
  const [runs, setRuns] = useState<AgentRun[]>([]);
  const [message, setMessage] = useState("");
  const [pending, setPending] = useState<Pending | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [revision, setRevision] = useState(0);
  const mounted = useRef(false);
  const sending = useRef(false);

  useEffect(() => {
    mounted.current = true;
    try {
      const stored = localStorage.getItem(storageKey);
      if (stored) {
        const value: unknown = JSON.parse(stored);
        if (value && typeof value === "object" && "key" in value && "message" in value && typeof value.key === "string" && typeof value.message === "string") {
          setPending({ key: value.key, message: value.message });
        } else {
          setError("Не удалось прочитать сохранённый запрос.");
        }
      }
    } catch {
      setError("Не удалось прочитать сохранённый запрос.");
    }
    return () => { mounted.current = false; };
  }, [storageKey]);

  useEffect(() => {
    let stopped = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const controller = new AbortController();
    const poll = async () => {
      try {
        const value: unknown = await request(`/api/tasks/${taskId}/runs`, { signal: controller.signal }, token, 200);
        if (!isAgentRunList(value, undefined, taskId)) throw new Error("invalid runs");
        if (stopped || localStorage.getItem("akhflow.token") !== token) return;
        setRuns(value);
        setError("");
        if (pending && value.some((run) => run.task_id === taskId && String(run.actor_id) === actorId && run.request_key === pending.key && run.message === pending.message)) {
          localStorage.removeItem(storageKey);
          setPending(null);
          setMessage("");
          void onChanged();
        }
      } catch {
        if (!stopped) setError("Не удалось обновить запуски агента.");
      }
      if (!stopped) timer = setTimeout(poll, 3000);
    };
    void poll();
    return () => { stopped = true; controller.abort(); if (timer) clearTimeout(timer); };
  }, [taskId, token, request, actorId, storageKey, pending, revision, onChanged]);

  const send = async (retry: Pending | null) => {
    if (sending.current || !actorId) return;
    const operation = retry ?? { key: crypto.randomUUID(), message: message.trim() };
    if (!operation.message) return;
    try {
      localStorage.setItem(storageKey, JSON.stringify(operation));
    } catch {
      setError("Не удалось сохранить запрос для восстановления.");
      return;
    }
    sending.current = true;
    setPending(operation);
    setBusy(true);
    try {
      const value: unknown = await request(`/api/tasks/${taskId}/runs`, {
        method: "POST",
        headers: { "Idempotency-Key": operation.key },
        body: JSON.stringify({ message: operation.message }),
      }, token, 202);
      if (!isAgentRunList([value], undefined, taskId)) throw new Error("invalid run");
      const accepted = value as AgentRun;
      if (accepted.request_key !== operation.key || String(accepted.actor_id) !== actorId || accepted.message !== operation.message) throw new Error("unexpected run");
      if (!mounted.current || localStorage.getItem("akhflow.token") !== token) return;
      localStorage.removeItem(storageKey);
      setPending(null);
      setMessage("");
      setRevision((value) => value + 1);
    } catch {
      if (mounted.current) setError("Не удалось подтвердить запуск. Проверь состояние или повтори запрос.");
    } finally {
      sending.current = false;
      if (mounted.current) setBusy(false);
    }
  };

  const cancel = async (id: number) => {
    try {
      await request(`/api/runs/${id}/cancel`, { method: "POST", body: "{}" }, token, 202);
      if (mounted.current) setRevision((value) => value + 1);
    } catch {
      if (mounted.current) setError("Не удалось остановить запуск.");
    }
  };

  return (
    <section className="space-y-2 border-t pt-3" aria-label="Агент для задачи">
      <h3 className="text-sm font-medium">Агент для задачи</h3>
      <p className="text-xs text-muted-foreground">Агент получит сохранённые заголовок и описание задачи.</p>
      <textarea aria-label="Запрос агенту" className="w-full rounded-md border bg-transparent p-2 text-sm" value={pending?.message ?? message} disabled={busy || !!pending} onChange={(event) => setMessage(event.target.value)} />
      <Button size="sm" disabled={busy || !actorId || (!pending && !message.trim())} onClick={() => void send(pending)}>
        {pending ? "Повторить запрос" : "Запустить агента"}
      </Button>
      {pending && <p role="status" className="text-sm">Проверяем, был ли принят запрос.</p>}
      {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
      <div aria-live="polite" className="space-y-2">
        {runs.map((run) => (
          <div key={run.id} className="space-y-1 rounded-md border p-2 text-sm">
            <p>{labels[run.status] ?? "Состояние неизвестно"}</p>
            <p className="whitespace-pre-wrap text-muted-foreground">{run.message}</p>
            {run.status === "running" && <p>Шаг {run.step.iteration} из {run.step.max_iterations}</p>}
            {run.steps.map((step) => <p key={step.sequence} className="text-muted-foreground">{step.summary}</p>)}
            {run.result?.reply && <p className="whitespace-pre-wrap">{run.result.reply}</p>}
            <Button size="sm" variant="ghost" onClick={() => onOpenChat(run.chat_id)}>Открыть чат</Button>
            {(run.status === "queued" || run.status === "running") && <Button size="sm" variant="outline" disabled={run.cancel_requested} onClick={() => void cancel(run.id)}>{run.cancel_requested ? "Отмена запрошена" : "Остановить"}</Button>}
          </div>
        ))}
      </div>
    </section>
  );
}
