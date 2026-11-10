import { useCallback, useEffect, useState } from "react";
import type { FormEvent } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

type Attachment = {
  id: number;
  filename: string;
  size_bytes: number;
};

type Props = {
  taskId: number;
  token: string | null;
  apiBase: string;
};

const maxFileSize = 10 * 1024 * 1024;

export function TaskAttachments({ taskId, token, apiBase }: Props) {
  const [attachments, setAttachments] = useState<Attachment[]>([]);
  const [file, setFile] = useState<File | null>(null);
  const [inputKey, setInputKey] = useState(0);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [loading, setLoading] = useState(true);
  const endpoint = `${apiBase}/api/tasks/${taskId}/attachments`;

  const load = useCallback(async (signal?: AbortSignal) => {
    const response = await fetch(endpoint, {
      headers: { Authorization: `Bearer ${token}` },
      signal,
    });
    if (!response.ok) {
      throw new Error("Не удалось загрузить список вложений.");
    }
    const data: Attachment[] = await response.json();
    setAttachments(data);
  }, [endpoint, token]);

  useEffect(() => {
    if (!token) return;
    const controller = new AbortController();
    setLoading(true);
    load(controller.signal).catch(() => {
      if (!controller.signal.aborted) {
        setError("Не удалось загрузить список вложений.");
      }
    }).finally(() => {
      if (!controller.signal.aborted) {
        setLoading(false);
      }
    });
    return () => controller.abort();
  }, [load, token]);

  const upload = async (event: FormEvent) => {
    event.preventDefault();
    if (!file || !token || busy) return;
    if (file.size > maxFileSize) {
      setError("Размер файла не должен превышать 10 МиБ.");
      return;
    }
    setBusy(true);
    setError("");
    const form = new FormData();
    form.append("file", file);
    try {
      const response = await fetch(endpoint, {
        method: "POST",
        headers: { Authorization: `Bearer ${token}` },
        body: form,
      });
      if (!response.ok) {
        throw new Error("upload failed");
      }
      setFile(null);
      setInputKey((value) => value + 1);
      try {
        await load();
      } catch {
        setError("Файл загружен, но список вложений не обновился.");
      }
    } catch {
      setError("Не удалось загрузить файл. Попробуй ещё раз.");
    } finally {
      setBusy(false);
    }
  };

  const download = async (attachment: Attachment) => {
    if (!token) return;
    setError("");
    try {
      const response = await fetch(`${endpoint}/${attachment.id}`, {
        headers: { Authorization: `Bearer ${token}` },
      });
      if (!response.ok) {
        throw new Error("download failed");
      }
      const blob = await response.blob();
      const url = URL.createObjectURL(blob);
      const link = document.createElement("a");
      link.href = url;
      link.download = attachment.filename;
      link.click();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
    } catch {
      setError("Не удалось скачать файл. Попробуй ещё раз.");
    }
  };

  return (
    <section aria-label="Вложения" className="space-y-2">
      <div className="text-xs text-muted-foreground">Вложения</div>
      {loading && <p className="text-sm text-muted-foreground">Загружаем вложения…</p>}
      {!loading && attachments.length === 0 && <p className="text-sm text-muted-foreground">Вложений пока нет.</p>}
      <ul className="space-y-1">
        {attachments.map((attachment) => (
          <li key={attachment.id}>
            <button className="text-left text-sm hover:underline" onClick={() => void download(attachment)}>
              {attachment.filename} ({attachment.size_bytes} байт)
            </button>
          </li>
        ))}
      </ul>
      <form onSubmit={upload} className="space-y-2">
        <label htmlFor="task-attachment" className="text-sm">Добавить файл, до 10 МиБ</label>
        <Input
          key={inputKey}
          id="task-attachment"
          type="file"
          disabled={busy}
          onChange={(event) => setFile(event.target.files?.[0] ?? null)}
        />
        <Button type="submit" size="sm" disabled={!file || busy}>
          {busy ? "Загружаем…" : "Загрузить"}
        </Button>
      </form>
      {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
    </section>
  );
}
