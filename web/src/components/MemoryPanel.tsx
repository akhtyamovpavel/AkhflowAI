import { useEffect, useRef, useState } from "react";
import { Button } from "@/components/ui/button";

type Memory = { id: number; scope: string; content: string; created_at: string };
type Request = (path: string, options?: RequestInit, token?: string | null) => Promise<any>;

function valid(value: unknown): value is Memory[] {
  if (!Array.isArray(value)) return false;
  return value.every((m) => m && typeof m === "object" && Number.isSafeInteger(m.id) && typeof m.scope === "string" && typeof m.content === "string");
}

export function MemoryPanel({ project, token, request }: { project: string; token: string; request: Request }) {
  const [open, setOpen] = useState(false);
  const [memories, setMemories] = useState<Memory[] | null>(null);
  const [scope, setScope] = useState("project");
  const [draft, setDraft] = useState("");
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const dialog = useRef<HTMLDialogElement>(null);
  const active = useRef(true);
  const controller = useRef<AbortController | null>(null);
  const pending = useRef(false);
  const base = `/api/projects/${encodeURIComponent(project)}/memories`;

  useEffect(() => {
    active.current = true;
    return () => {
      active.current = false;
      controller.current?.abort();
    };
  }, []);

  useEffect(() => {
    if (!open || !dialog.current) return;
    const element = dialog.current;
    element.showModal();
    return () => element.close();
  }, [open]);

  const current = () => active.current && localStorage.getItem("akhflow.token") === token;

  const load = async () => {
    if (pending.current) return;
    pending.current = true;
    setBusy(true);
    const signal = new AbortController();
    controller.current = signal;
    try {
      const data = await request(base, { signal: signal.signal }, token);
      if (!valid(data)) throw new Error("invalid response");
      if (!current()) return;
      setMemories(data);
      setMessage("");
    } catch {
      if (current() && !signal.signal.aborted) setMessage("Не удалось загрузить память.");
    } finally {
      pending.current = false;
      if (current()) setBusy(false);
    }
  };

  const add = async () => {
    if (pending.current || !draft.trim()) return;
    pending.current = true;
    setBusy(true);
    try {
      await request(base, {
        method: "POST",
        body: JSON.stringify({ scope, content: draft }),
      }, token);
      if (!current()) return;
      setDraft("");
      setMessage("Запись сохранена.");
      await load();
    } catch {
      if (current()) setMessage("Не удалось сохранить запись.");
    } finally {
      pending.current = false;
      if (current()) setBusy(false);
    }
  };

  const remove = async (id: number) => {
    if (pending.current) return;
    pending.current = true;
    setBusy(true);
    try {
      await request(`${base}/${id}`, { method: "DELETE" }, token);
      if (!current()) return;
      setMemories((prev) => (prev ? prev.filter((m) => m.id !== id) : prev));
      setMessage("Запись удалена.");
    } catch {
      if (current()) setMessage("Не удалось удалить запись.");
    } finally {
      pending.current = false;
      if (current()) setBusy(false);
    }
  };

  return (
    <>
      <Button size="sm" variant="outline" onClick={() => {
        setOpen(true);
        if (memories === null) void load();
      }}>Память</Button>
      {open && (
        <dialog ref={dialog} aria-labelledby="memory-title" onCancel={() => setOpen(false)} className="m-auto w-full max-w-xl rounded-lg bg-background p-4 text-foreground shadow-lg backdrop:bg-black/40">
          <div className="space-y-3">
            <h2 id="memory-title" className="font-semibold">Память проекта</h2>
            <p className="text-sm text-muted-foreground">Решения, договорённости и стиль, подмешиваемые в контекст агента.</p>
            <div className="flex gap-2">
              <select className="h-8 rounded-md border bg-transparent px-2 text-sm" value={scope} onChange={(e) => setScope(e.target.value)}>
                <option value="project">Проект</option>
                <option value="user">Я</option>
              </select>
              <input
                className="h-8 flex-1 rounded-md border bg-background px-2 text-sm"
                placeholder="Новая запись…"
                value={draft}
                onChange={(e) => setDraft(e.target.value)}
              />
              <Button size="sm" disabled={busy || !draft.trim()} onClick={() => void add()}>Добавить</Button>
            </div>
            {message && <p role="status" className="text-sm">{message}</p>}
            {memories === null && <Button size="sm" variant="outline" disabled={busy} onClick={() => void load()}>Повторить загрузку</Button>}
            {memories !== null && memories.length === 0 && <p className="text-sm text-muted-foreground">Память пуста.</p>}
            {memories !== null && memories.map((m) => (
              <div key={m.id} className="flex items-start justify-between gap-2 rounded-md border p-2">
                <div className="space-y-1">
                  <span className="text-xs text-muted-foreground">{m.scope === "project" ? "Проект" : "Я"}</span>
                  <p className="whitespace-pre-wrap text-sm">{m.content}</p>
                </div>
                <Button size="sm" variant="ghost" disabled={busy} onClick={() => void remove(m.id)}>Удалить</Button>
              </div>
            ))}
            <div className="flex gap-2">
              <Button size="sm" variant="outline" onClick={() => setOpen(false)}>Закрыть</Button>
            </div>
          </div>
        </dialog>
      )}
    </>
  );
}
