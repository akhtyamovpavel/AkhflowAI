import { useEffect, useRef, useState } from "react";
import { Button } from "@/components/ui/button";

type Instructions = { instructions: string; version: number; updated_at: string | null };
type Request = (path: string, options?: RequestInit, token?: string | null) => Promise<any>;

function valid(value: unknown): value is Instructions {
  if (!value || typeof value !== "object") return false;
  const data = value as Record<string, unknown>;
  return typeof data.instructions === "string" && Number.isSafeInteger(data.version) && (data.version as number) >= 0 &&
    (typeof data.updated_at === "string" || data.updated_at === null);
}

export function AgentInstructions({ project, token, request }: { project: string; token: string; request: Request }) {
  const [open, setOpen] = useState(false);
  const [role, setRole] = useState("");
  const [draft, setDraft] = useState("");
  const [version, setVersion] = useState<number | null>(null);
  const [latest, setLatest] = useState<Instructions | null>(null);
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const dialog = useRef<HTMLDialogElement>(null);
  const active = useRef(true);
  const controller = useRef<AbortController | null>(null);
  const pending = useRef(false);
  const path = `/api/projects/${encodeURIComponent(project)}/agent/instructions`;

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
  const load = async (initial: boolean) => {
    if (pending.current) return;
    pending.current = true;
    setBusy(true);
    const signal = new AbortController();
    controller.current = signal;
    try {
      const [data, rights] = await Promise.all([
        request(path, { signal: signal.signal }, token),
        request(`/api/projects/${encodeURIComponent(project)}/role`, { signal: signal.signal }, token),
      ]);
      if (!valid(data) || typeof rights?.role !== "string") throw new Error("invalid response");
      if (!current()) return;
      setRole(rights.role);
      if (initial) {
        setDraft(data.instructions);
        setVersion(data.version);
        setLatest(null);
      } else {
        setLatest(data);
      }
      setMessage("");
    } catch {
      if (current() && !signal.signal.aborted) setMessage("Не удалось загрузить инструкции. Черновик сохранён.");
    } finally {
      pending.current = false;
      if (current()) setBusy(false);
    }
  };

  const save = async () => {
    if (pending.current || version === null) return;
    if (new TextEncoder().encode(draft).length > 16384) {
      setMessage("Инструкции должны занимать не более 16 КиБ.");
      return;
    }
    pending.current = true;
    setBusy(true);
    const signal = new AbortController();
    controller.current = signal;
    const submitted = draft;
    try {
      const data = await request(path, {
        method: "PUT",
        body: JSON.stringify({ instructions: submitted, version }),
        signal: signal.signal,
      }, token);
      if (!valid(data)) throw new Error("invalid response");
      if (!current()) return;
      setVersion(data.version);
      setLatest(null);
      setMessage("Инструкции сохранены.");
    } catch (error) {
      if (!current() || signal.signal.aborted) return;
      if (error instanceof Error && error.message.startsWith("409:")) {
        setMessage("Инструкции изменились. Загрузи актуальную версию и сравни её с черновиком.");
      } else {
        setMessage("Не удалось подтвердить сохранение. Черновик сохранён; загрузи актуальную версию перед повтором.");
      }
    } finally {
      pending.current = false;
      if (current()) setBusy(false);
    }
  };

  const canWrite = role === "owner" || role === "admin";
  return (
    <>
      <Button size="sm" variant="outline" onClick={() => {
        setOpen(true);
        if (version === null) void load(true);
      }}>Инструкции агента</Button>
      {open && (
        <dialog ref={dialog} aria-labelledby="agent-instructions-title" onCancel={() => setOpen(false)} className="m-auto w-full max-w-xl rounded-lg bg-background p-4 text-foreground shadow-lg backdrop:bg-black/40">
          <div className="space-y-3">
            <h2 id="agent-instructions-title" className="font-semibold">Инструкции агента</h2>
            <p className="text-sm text-muted-foreground">Инструкции применяются к новым запускам агента в проекте.</p>
            <label htmlFor="agent-instructions-text" className="text-sm">Текст инструкций</label>
            <textarea id="agent-instructions-text" className="min-h-40 w-full rounded-md border bg-background p-2 text-sm" value={draft} readOnly={!canWrite} onChange={(event) => setDraft(event.target.value)} />
            {message && <p role="status" className="text-sm">{message}</p>}
            {version === null && <Button size="sm" variant="outline" disabled={busy} onClick={() => void load(true)}>Повторить загрузку</Button>}
            {version !== null && canWrite && <Button size="sm" variant="outline" disabled={busy} onClick={() => void load(false)}>Загрузить актуальную версию</Button>}
            {latest && (
              <div className="space-y-2 rounded-md border p-2">
                <p className="text-sm">Актуальная версия {latest.version}:</p>
                <p className="whitespace-pre-wrap text-sm">{latest.instructions || "Инструкции пусты"}</p>
                <Button size="sm" variant="outline" onClick={() => {
                  setVersion(latest.version);
                  setLatest(null);
                  setMessage("Актуальная версия выбрана. Черновик сохранён; проверь его перед сохранением.");
                }}>Использовать версию для черновика</Button>
              </div>
            )}
            <div className="flex gap-2">
              {canWrite && <Button size="sm" disabled={busy || version === null || latest !== null} onClick={() => void save()}>Сохранить</Button>}
              <Button size="sm" variant="outline" onClick={() => setOpen(false)}>Закрыть</Button>
            </div>
          </div>
        </dialog>
      )}
    </>
  );
}
