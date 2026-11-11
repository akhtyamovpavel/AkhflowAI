import { useCallback, useEffect, useRef, useState } from "react";
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
  project: string;
};

const maxFileSize = 10 * 1024 * 1024;

export function TaskAttachments({ taskId, token, apiBase, project }: Props) {
  const [attachments, setAttachments] = useState<Attachment[]>([]);
  const [file, setFile] = useState<File | null>(null);
  const [inputKey, setInputKey] = useState(0);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [loading, setLoading] = useState(true);
  const [role, setRole] = useState("");
  const roleRequest = useRef(0);
  const attachmentRequest = useRef(0);
  const endpoint = `${apiBase}/api/tasks/${taskId}/attachments`;
  const canUpload = ["owner", "admin", "editor", "member"].includes(role);

  const refreshRole = useCallback(async () => {
    const current = ++roleRequest.current;
    try {
      const response = await fetch(`${apiBase}/api/projects/${encodeURIComponent(project)}/role`, {
        headers: { Authorization: `Bearer ${token}` },
      });
      if (!response.ok) throw new Error("role lookup failed");
      const data: { role: string } = await response.json();
      if (roleRequest.current === current) setRole(data.role);
    } catch {
      if (roleRequest.current === current) setRole("");
    }
  }, [apiBase, project, token]);

  useEffect(() => {
    setRole("");
    if (!token || !project) return;
    void refreshRole();
    const refresh = () => { void refreshRole(); };
    window.addEventListener("focus", refresh);
    document.addEventListener("visibilitychange", refresh);
    return () => {
      roleRequest.current++;
      window.removeEventListener("focus", refresh);
      document.removeEventListener("visibilitychange", refresh);
    };
  }, [project, token, refreshRole]);

  useEffect(() => {
    if (!canUpload) setFile(null);
  }, [canUpload]);

  const load = useCallback(async (signal?: AbortSignal, afterUpload = false) => {
    const current = ++attachmentRequest.current;
    setLoading(true);
    setError("");
    try {
      const response = await fetch(endpoint, {
        headers: { Authorization: `Bearer ${token}` },
        signal,
      });
      if (!response.ok) {
        throw new Error("attachment lookup failed");
      }
      const data: Attachment[] = await response.json();
      if (attachmentRequest.current === current && !signal?.aborted) {
        setAttachments(data);
      }
    } catch {
      if (attachmentRequest.current === current && !signal?.aborted) {
        if (afterUpload) {
          setError("Файл загружен, но список вложений не обновился.");
        } else {
          setError("Не удалось загрузить список вложений.");
        }
      }
    } finally {
      if (attachmentRequest.current === current && !signal?.aborted) {
        setLoading(false);
      }
    }
  }, [endpoint, token]);

  useEffect(() => {
    if (!token) return;
    const controller = new AbortController();
    void load(controller.signal);
    return () => {
      attachmentRequest.current++;
      controller.abort();
    };
  }, [load, token]);

  const upload = async (event: FormEvent) => {
    event.preventDefault();
    if (!file || !token || busy || !canUpload) return;
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
        if (response.status === 403) void refreshRole();
        throw new Error("upload failed");
      }
      setFile(null);
      setInputKey((value) => value + 1);
      await load(undefined, true);
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
      {canUpload && <form onSubmit={upload} className="space-y-2">
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
      </form>}
      {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
    </section>
  );
}
