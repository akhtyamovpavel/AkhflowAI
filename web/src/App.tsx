import { useCallback, useEffect, useRef, useState } from "react";
import type { KeyboardEvent as ReactKeyboardEvent } from "react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { DeadlineReminders } from "@/components/DeadlineReminders";
import { EmailNotifications } from "@/components/EmailNotifications";
import { ProjectDashboard } from "@/components/ProjectDashboard";
import { KanbanSquare, ListTodo, MessageSquare, Plus, Send, Layers, Link2, X, ChartNoAxesCombined } from "lucide-react";

type Task = {
  id: number;
  title: string;
  status: string;
  priority: string;
  description?: string;
  labels?: string[];
  due_date?: string;
  epic_id?: number | null;
  assignee_id?: number | null;
  author_id?: number;
  estimate_min?: number | null;
  depends_on?: number[];
  blocks?: number[];
};

type Project = { id: number; slug: string; name: string };

type ChatSession = { id: number; title: string; created_at: string };

type ChatMessage = {
  id: number;
  role: string;
  content: string;
  tool_calls?: { tool: string; args: string; result: string }[];
};

type Epic = {
  id: number;
  title: string;
  description: string;
  status: string;
  created_at: string;
};

type Comment = {
  id: number;
  task_id: number;
  author_id: number;
  body: string;
  created_at: string;
};

type AuditEntry = {
  id: number;
  actor: string;
  action: string;
  entity: string;
  entity_id?: number;
  payload?: Record<string, any>;
  created_at: string;
};

type Member = {
  id: number;
  email: string;
  display_name: string;
};

const COLUMNS = [
  { id: "backlog", label: "Backlog" },
  { id: "todo", label: "Todo" },
  { id: "in_progress", label: "In Progress" },
  { id: "done", label: "Done" },
];

const PRIORITY_COLORS: Record<string, string> = {
  high: "bg-destructive/10 text-destructive",
  medium: "bg-secondary text-secondary-foreground",
  low: "bg-muted text-muted-foreground",
};

const FIELD_LABELS: Record<string, string> = {
  title: "название",
  status: "статус",
  priority: "приоритет",
  description: "описание",
  due_date: "дедлайн",
  epic_id: "эпик",
  assignee_id: "исполнитель",
  estimate_min: "оценка",
  labels: "метки",
  recurring: "повтор",
};

const statusLabel = (id: string) => COLUMNS.find((c) => c.id === id)?.label ?? id;

function auditValue(key: string, value: any): string {
  if (value === null || value === undefined || value === "") return "—";
  if (key === "status") return statusLabel(String(value));
  if (Array.isArray(value)) return value.length ? value.join(", ") : "—";
  const s = String(value);
  return s.length > 40 ? `${s.slice(0, 40)}…` : s;
}

function auditText(e: AuditEntry): string {
  const p = e.payload ?? {};
  switch (e.action) {
    case "create_task":
      return "задача создана";
    case "move_task":
      return `статус → ${statusLabel(String(p.status ?? ""))}`;
    case "update_task": {
      const parts = Object.entries(p).map(([k, v]) => `${FIELD_LABELS[k] ?? k} → ${auditValue(k, v)}`);
      return parts.length ? `изменена: ${parts.join(", ")}` : "изменена";
    }
    case "add_dependency":
      return `добавлена зависимость от #${p.depends_on ?? "?"}`;
    case "remove_dependency":
      return `удалена зависимость от #${p.depends_on ?? "?"}`;
    case "recur_task": {
      const from = p.master ?? p.from;
      return from ? `повторение: создана задача от #${from}` : "повторение: создана новая задача";
    }
    case "delete_task":
      return "задача удалена";
    default:
      return e.action;
  }
}

const API = (import.meta as any).env?.VITE_API_URL ?? "";

function briefToolResult(raw: string): string {
  try {
    const r = JSON.parse(raw);
    if (typeof r?.message === "string" && r.message) return r.message;
    if (Array.isArray(r?.data)) return `${r.data.length} записей`;
    const entries = Object.entries(r ?? {});
    if (entries.length > 0 && entries.length <= 4) {
      return entries.map(([k, v]) => `${k}=${v}`).join(", ");
    }
  } catch {
    /* keep raw */
  }
  return raw.length > 100 ? `${raw.slice(0, 100)}…` : raw;
}

function useToken() {
  const [token, setToken] = useState<string | null>(() => localStorage.getItem("akhflow.token"));
  const save = (t: string | null) => {
    if (t) localStorage.setItem("akhflow.token", t);
    else localStorage.removeItem("akhflow.token");
    setToken(t);
  };
  return [token, save] as const;
}

async function api(path: string, opts: RequestInit = {}, token?: string | null) {
  const res = await fetch(API + path, {
    ...opts,
    headers: {
      "Content-Type": "application/json",
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
      ...(opts.headers || {}),
    },
  });
  if (!res.ok) {
    if (res.status === 401 && token) {
      localStorage.removeItem("akhflow.token");
      window.location.reload();
    }
    const body = await res.text();
    throw new Error(`${res.status}: ${body}`);
  }
  return res.json();
}

function AkhcheckImport({ project, token, onImported }: { project: string; token: string | null; onImported: () => Promise<void> }) {
  const [open, setOpen] = useState(false);
  const [file, setFile] = useState<File | null>(null);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");

  const submit = async (event: React.FormEvent) => {
    event.preventDefault();
    if (!file || !project || !token) return;
    setBusy(true);
    setMessage("");
    try {
      if (file.size > 4 * 1024 * 1024) throw new Error("Файл больше 4 МБ");
      const course = JSON.parse(await file.text());
      const result = await api(`/api/projects/${encodeURIComponent(project)}/imports/akhcheck`, {
        method: "POST",
        body: JSON.stringify(course),
      }, token);
      await onImported();
      setMessage(`Добавлено: ${result.created}, уже было: ${result.skipped}`);
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "Не удалось импортировать файл");
    } finally {
      setBusy(false);
    }
  };

  if (!open) return <Button size="sm" variant="outline" onClick={() => setOpen(true)}>Импорт Akhcheck</Button>;
  return (
    <form className="flex flex-wrap items-center gap-2" onSubmit={submit}>
      <label className="text-sm" htmlFor="akhcheck-course-file">JSON курса Akhcheck</label>
      <input id="akhcheck-course-file" type="file" accept="application/json,.json" onChange={(event) => setFile(event.target.files?.[0] ?? null)} />
      <span className="text-xs text-muted-foreground">Сохрани JSON со страницы /api/teaching/course/ID в Akhcheck.</span>
      <Button size="sm" type="submit" disabled={!file || busy}>Импортировать</Button>
      <Button size="sm" variant="ghost" type="button" onClick={() => setOpen(false)}>Закрыть</Button>
      {message && <span className="text-sm" role="status">{message}</span>}
    </form>
  );
}

function Login({ onLogin }: { onLogin: (t: string) => void }) {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [name, setName] = useState("");
  const [mode, setMode] = useState<"login" | "register">("login");
  const [err, setErr] = useState("");
  const submit = async () => {
    setErr("");
    try {
      const path = mode === "login" ? "/api/auth/login" : "/api/auth/register";
      const body: any = { email, password };
      if (mode === "register") body.display_name = name || email;
      const data = await api(path, { method: "POST", body: JSON.stringify(body) });
      onLogin(data.token);
    } catch (e: any) {
      setErr(e.message);
    }
  };
  return (
    <div className="min-h-screen flex items-center justify-center bg-background">
      <Card className="w-full max-w-sm">
        <CardHeader>
          <CardTitle>Akhflow-AI</CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          {mode === "register" && (
            <Input placeholder="Имя" value={name} onChange={(e) => setName(e.target.value)} />
          )}
          <Input type="email" placeholder="Email" value={email} onChange={(e) => setEmail(e.target.value)} />
          <Input type="password" placeholder="Пароль" value={password} onChange={(e) => setPassword(e.target.value)} />
          {err && <p className="text-sm text-destructive">{err}</p>}
          <Button className="w-full" onClick={submit}>
            {mode === "login" ? "Войти" : "Создать аккаунт"}
          </Button>
          <Button variant="ghost" className="w-full" onClick={() => setMode(mode === "login" ? "register" : "login")}>
            {mode === "login" ? "Нет аккаунта? Зарегистрироваться" : "Есть аккаунт? Войти"}
          </Button>
        </CardContent>
      </Card>
    </div>
  );
}

function Kanban({ tasks, epics, onOpen, onMove }: { tasks: Task[]; epics: Epic[]; onOpen: (id: number) => void; onMove: (id: number, dir: number) => Promise<void> }) {
  const taskButtons = useRef(new Map<number, HTMLButtonElement>());
  const pendingFocus = useRef<{ id: number; status: string } | null>(null);
  const moving = useRef(false);
  const [moveError, setMoveError] = useState("");
  const idx = (s: string) => COLUMNS.findIndex((c) => c.id === s);
  useEffect(() => {
    const pending = pendingFocus.current;
    if (!pending) return;
    const task = tasks.find((item) => item.id === pending.id);
    if (task?.status === pending.status) {
      if (document.activeElement === document.body) {
        taskButtons.current.get(pending.id)?.focus();
      }
      pendingFocus.current = null;
    }
  }, [tasks]);

  const moveTask = async (task: Task, direction: number, restoreFocus: boolean) => {
    const target = COLUMNS[idx(task.status) + direction];
    if (!target || moving.current) return;
    moving.current = true;
    setMoveError("");
    if (restoreFocus) {
      pendingFocus.current = { id: task.id, status: target.id };
    }
    try {
      await onMove(task.id, direction);
    } catch (error: unknown) {
      pendingFocus.current = null;
      const message = error instanceof Error ? error.message : "Не удалось переместить задачу";
      setMoveError(message);
    } finally {
      moving.current = false;
    }
  };

  const handleTaskKey = (event: ReactKeyboardEvent<HTMLButtonElement>, task: Task) => {
    if (event.repeat || event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return;
    if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
    event.preventDefault();
    const direction = event.key === "ArrowLeft" ? -1 : 1;
    void moveTask(task, direction, true);
  };
  const overdue = (t: Task) =>
    !!t.due_date && t.status !== "done" && new Date(`${t.due_date}T23:59:59`) < new Date();
  const blockedCount = (t: Task) =>
    (t.depends_on ?? []).filter((id) => {
      const dep = tasks.find((x) => x.id === id);
      return dep && dep.status !== "done";
    }).length;
  return (
    <>
    <p id="kanban-keyboard-help" className="text-sm text-muted-foreground">Выбери название карточки и нажми ← или →, чтобы изменить её статус.</p>
    {moveError && <p role="alert" className="text-sm text-destructive">{moveError}</p>}
    <div className="grid grid-cols-4 gap-4">
      {COLUMNS.map((col) => (
        <div key={col.id} className="space-y-2">
          <h3 className="text-sm font-medium text-muted-foreground uppercase tracking-wide">{col.label}</h3>
          {tasks
            .filter((t) => t.status === col.id)
            .map((t) => {
              const i = idx(t.status);
              const waiting = blockedCount(t);
              return (
                <Card key={t.id}>
                  <CardContent className="p-3 space-y-2">
                    <button
                      ref={(element) => {
                        if (element) taskButtons.current.set(t.id, element);
                        else taskButtons.current.delete(t.id);
                      }}
                      aria-describedby="kanban-keyboard-help"
                      aria-keyshortcuts="ArrowLeft ArrowRight"
                      className="text-left text-sm font-medium hover:underline"
                      onClick={() => onOpen(t.id)}
                      onKeyDown={(event) => handleTaskKey(event, t)}
                    >
                      {t.title}
                    </button>
                    <div className="flex items-center justify-between">
                      <span className={`text-xs px-1.5 py-0.5 rounded ${PRIORITY_COLORS[t.priority] ?? PRIORITY_COLORS.medium}`}>
                        {t.priority}
                      </span>
                      <span className="text-xs text-muted-foreground">#{t.id}</span>
                    </div>
                    {t.epic_id != null && (
                      <span className="text-xs text-muted-foreground">
                        {epics.find((e) => e.id === t.epic_id)?.title}
                      </span>
                    )}
                    {(t.depends_on?.length ?? 0) > 0 && (
                      <div className="flex items-center gap-2 text-xs">
                        <span className="inline-flex items-center gap-1 text-muted-foreground">
                          <Link2 className="h-3 w-3" />{t.depends_on!.length}
                        </span>
                        {waiting > 0 && <span className="text-amber-600">ждёт: {waiting}</span>}
                      </div>
                    )}
                    {(t.due_date || t.estimate_min != null) && (
                      <div className="flex items-center gap-2 text-xs text-muted-foreground">
                        {t.due_date && <span className={overdue(t) ? "text-destructive" : ""}>{t.due_date}</span>}
                        {t.estimate_min != null && <span>{t.estimate_min} мин</span>}
                      </div>
                    )}
                    <div className="flex gap-1">
                      <Button variant="outline" size="sm" aria-label={`Переместить ${t.title} влево`} disabled={i === 0} onClick={() => void moveTask(t, -1, false)}>
                        ←
                      </Button>
                      <Button variant="outline" size="sm" aria-label={`Переместить ${t.title} вправо`} disabled={i === COLUMNS.length - 1} onClick={() => void moveTask(t, 1, false)}>
                        →
                      </Button>
                    </div>
                  </CardContent>
                </Card>
              );
            })}
        </div>
      ))}
    </div>
    </>
  );
}

function TaskDetail({ taskId, epics, members, tasks, onOpen, onClose, onChanged }: {
  taskId: number;
  epics: Epic[];
  members: Member[];
  tasks: Task[];
  onOpen: (id: number) => void;
  onClose: () => void;
  onChanged: () => void;
}) {
  const [task, setTask] = useState<Task | null>(null);
  const [comments, setComments] = useState<Comment[]>([]);
  const [deps, setDeps] = useState<Task[]>([]);
  const [blocks, setBlocks] = useState<Task[]>([]);
  const [history, setHistory] = useState<AuditEntry[]>([]);
  const [desc, setDesc] = useState("");
  const [comment, setComment] = useState("");
  const [newDep, setNewDep] = useState("");
  const token = localStorage.getItem("akhflow.token");

  const load = useCallback(async () => {
    if (!token) return;
    const data = await api(`/api/tasks/${taskId}`, {}, token);
    setTask(data.task);
    setComments(data.comments ?? []);
    setDeps(data.dependencies ?? []);
    setBlocks(data.blocks ?? []);
    setHistory(data.history ?? []);
    setDesc(data.task.description ?? "");
  }, [taskId, token]);

  useEffect(() => {
    load();
  }, [load]);

  if (!task) return null;

  const patch = async (body: unknown) => {
    await api(`/api/tasks/${taskId}`, { method: "PATCH", body: JSON.stringify(body) }, token);
    await load();
    onChanged();
  };

  const addComment = async () => {
    if (!comment.trim()) return;
    await api(`/api/tasks/${taskId}/comments`, {
      method: "POST",
      body: JSON.stringify({ body: comment.trim() }),
    }, token);
    setComment("");
    await load();
  };

  const addDep = async () => {
    if (!newDep) return;
    await api(`/api/tasks/${taskId}/dependencies`, {
      method: "POST",
      body: JSON.stringify({ depends_on: Number(newDep) }),
    }, token);
    setNewDep("");
    await load();
    onChanged();
  };

  const removeDep = async (depId: number) => {
    await api(`/api/tasks/${taskId}/dependencies/${depId}`, { method: "DELETE" }, token);
    await load();
    onChanged();
  };

  const depRow = (d: Task) => (
    <div key={d.id} className="flex items-center justify-between gap-2 rounded-md border p-2">
      <button className="text-left text-sm hover:underline" onClick={() => onOpen(d.id)}>
        #{d.id} {d.title}
      </button>
      <span className={`shrink-0 text-xs px-1.5 py-0.5 rounded ${d.status === "done" ? "bg-secondary text-secondary-foreground" : "bg-amber-500/10 text-amber-600"}`}>
        {d.status}
      </span>
      <Button size="sm" variant="ghost" onClick={() => removeDep(d.id)}><X /></Button>
    </div>
  );

  const depOptions = tasks.filter(
    (t) => t.id !== taskId && !deps.some((d) => d.id === t.id),
  );

  const authorName = (id?: number | null) =>
    members.find((m) => m.id === id)?.display_name || (id ? `user #${id}` : "—");

  return (
    <div className="fixed inset-0 z-50" onClick={onClose}>
      <div className="absolute inset-0 bg-black/30" />
      <div
        className="absolute inset-y-0 right-0 w-full max-w-md overflow-y-auto border-l bg-background shadow-xl"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between border-b p-4">
          <span className="text-sm text-muted-foreground">Задача #{task.id}</span>
          <Button size="sm" variant="ghost" onClick={onClose}><X /></Button>
        </div>
        <div className="space-y-4 p-4">
          <Input
            value={task.title}
            onChange={(e) => setTask({ ...task, title: e.target.value })}
            onBlur={() => patch({ title: task.title })}
            onKeyDown={(e) => e.key === "Enter" && (e.target as HTMLInputElement).blur()}
            className="text-base font-medium"
          />
          <div className="grid grid-cols-2 gap-2">
            <label className="text-xs text-muted-foreground">Статус
              <select
                className="mt-1 w-full rounded-md border bg-transparent px-2 py-1.5 text-sm"
                value={task.status}
                onChange={(e) => patch({ status: e.target.value })}
              >
                {COLUMNS.map((c) => <option key={c.id} value={c.id}>{c.label}</option>)}
              </select>
            </label>
            <label className="text-xs text-muted-foreground">Приоритет
              <select
                className="mt-1 w-full rounded-md border bg-transparent px-2 py-1.5 text-sm"
                value={task.priority}
                onChange={(e) => patch({ priority: e.target.value })}
              >
                {Object.keys(PRIORITY_COLORS).map((p) => <option key={p} value={p}>{p}</option>)}
              </select>
            </label>
            <label className="text-xs text-muted-foreground">Эпик
              <select
                className="mt-1 w-full rounded-md border bg-transparent px-2 py-1.5 text-sm"
                value={task.epic_id ?? ""}
                onChange={(e) => patch({ epic_id: e.target.value ? Number(e.target.value) : null })}
              >
                <option value="">—</option>
                {epics.map((e) => <option key={e.id} value={e.id}>{e.title}</option>)}
              </select>
            </label>
            <label className="text-xs text-muted-foreground">Исполнитель
              <select
                className="mt-1 w-full rounded-md border bg-transparent px-2 py-1.5 text-sm"
                value={task.assignee_id ?? ""}
                onChange={(e) => patch({ assignee_id: e.target.value ? Number(e.target.value) : null })}
              >
                <option value="">—</option>
                {members.map((m) => <option key={m.id} value={m.id}>{m.display_name}</option>)}
              </select>
            </label>
            <label className="text-xs text-muted-foreground">Дедлайн
              <input
                type="date"
                className="mt-1 w-full rounded-md border bg-transparent px-2 py-1.5 text-sm"
                value={task.due_date ?? ""}
                onChange={(e) => patch({ due_date: e.target.value || null })}
              />
            </label>
            <label className="text-xs text-muted-foreground">Оценка, мин
              <input
                type="number"
                className="mt-1 w-full rounded-md border bg-transparent px-2 py-1.5 text-sm"
                value={task.estimate_min ?? ""}
                onChange={(e) => patch({ estimate_min: e.target.value ? Number(e.target.value) : null })}
              />
            </label>
          </div>
          <div>
            <div className="mb-1 text-xs text-muted-foreground">Описание</div>
            <textarea
              className="min-h-24 w-full rounded-md border bg-transparent p-2 text-sm"
              value={desc}
              onChange={(e) => setDesc(e.target.value)}
              placeholder="Описание задачи..."
            />
            <Button size="sm" variant="outline" onClick={() => patch({ description: desc })}>Сохранить</Button>
          </div>
          <div>
            <div className="mb-1 text-xs text-muted-foreground">Зависит от ({deps.length})</div>
            <div className="space-y-1">
              {deps.length === 0 && <p className="text-sm text-muted-foreground">Пока пусто</p>}
              {deps.map(depRow)}
            </div>
            {depOptions.length > 0 && (
              <div className="mt-2 flex gap-2">
                <select
                  className="min-w-0 flex-1 rounded-md border bg-transparent px-2 py-1.5 text-sm"
                  value={newDep}
                  onChange={(e) => setNewDep(e.target.value)}
                >
                  <option value="">Выберите задачу...</option>
                  {depOptions.map((t) => (
                    <option key={t.id} value={t.id}>#{t.id} {t.title}</option>
                  ))}
                </select>
                <Button size="sm" onClick={addDep} disabled={!newDep}>Добавить</Button>
              </div>
            )}
          </div>
          {blocks.length > 0 && (
            <div>
              <div className="mb-1 text-xs text-muted-foreground">Блокирует ({blocks.length})</div>
              <div className="space-y-1">{blocks.map(depRow)}</div>
            </div>
          )}
          <div>
            <div className="mb-1 text-xs text-muted-foreground">Комментарии ({comments.length})</div>
            <div className="space-y-2">
              {comments.length === 0 && <p className="text-sm text-muted-foreground">Пока пусто</p>}
              {comments.map((c) => (
                <div key={c.id} className="rounded-md border p-2">
                  <div className="flex justify-between text-xs text-muted-foreground">
                    <span>{authorName(c.author_id)}</span>
                    <span>{new Date(c.created_at).toLocaleString("ru-RU")}</span>
                  </div>
                  <p className="whitespace-pre-wrap text-sm">{c.body}</p>
                </div>
              ))}
            </div>
            <div className="mt-2 flex gap-2">
              <Input
                value={comment}
                onChange={(e) => setComment(e.target.value)}
                onKeyDown={(e) => e.key === "Enter" && addComment()}
                placeholder="Комментарий..."
              />
              <Button size="sm" onClick={addComment}><Send /></Button>
            </div>
          </div>
          <div>
            <div className="mb-1 text-xs text-muted-foreground">История ({history.length})</div>
            <div className="space-y-1">
              {history.length === 0 && <p className="text-sm text-muted-foreground">Пока пусто</p>}
              {history.map((h) => (
                <div key={h.id} className="rounded-md border p-2 text-sm">
                  <div className="flex justify-between text-xs text-muted-foreground">
                    <span>{h.actor}</span>
                    <span>{new Date(h.created_at).toLocaleString("ru-RU")}</span>
                  </div>
                  <p>{auditText(h)}</p>
                </div>
              ))}
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}

export default function App() {
  const [token, saveToken] = useToken();
  const [view, setView] = useState<"kanban" | "list" | "dashboard" | "agent">("kanban");
  const [projects, setProjects] = useState<Project[]>([]);
  const [project, setProject] = useState("");
  const [tasks, setTasks] = useState<Task[]>([]);
  const [chats, setChats] = useState<ChatSession[]>([]);
  const [activeChat, setActiveChat] = useState<number | null>(null);
  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const [input, setInput] = useState("");
  const [newTitle, setNewTitle] = useState("");
  const [showNewProject, setShowNewProject] = useState(false);
  const [newProjectName, setNewProjectName] = useState("");
  const [epics, setEpics] = useState<Epic[]>([]);
  const [epicFilter, setEpicFilter] = useState<string>("all");
  const [showNewEpic, setShowNewEpic] = useState(false);
  const [newEpicTitle, setNewEpicTitle] = useState("");
  const [showTrelloImport, setShowTrelloImport] = useState(false);
  const [trelloBoard, setTrelloBoard] = useState("");
  const [trelloResult, setTrelloResult] = useState("");
  const [trelloBusy, setTrelloBusy] = useState(false);
  const [members, setMembers] = useState<Member[]>([]);
  const [openTaskId, setOpenTaskId] = useState<number | null>(null);
  const [busy, setBusy] = useState(false);
  const [showBitrixImport, setShowBitrixImport] = useState(false);
  const [bitrixGroup, setBitrixGroup] = useState("");
  const [bitrixBusy, setBitrixBusy] = useState(false);
  const [bitrixMessage, setBitrixMessage] = useState("");
  const newTaskInput = useRef<HTMLInputElement>(null);

  useEffect(() => {
    const focusNewTask = (event: KeyboardEvent) => {
      if (!token || !project || openTaskId || showNewProject || showNewEpic || showTrelloImport || showBitrixImport) return;
      if (!event.altKey || event.ctrlKey || event.metaKey || event.shiftKey || event.repeat || event.code !== "KeyN") return;
      const target = event.target;
      if (target instanceof Element && target.closest("form, input, textarea, select, [contenteditable]:not([contenteditable='false']), [role='dialog']")) return;
      event.preventDefault();
      newTaskInput.current?.focus();
    };
    window.addEventListener("keydown", focusNewTask);
    return () => window.removeEventListener("keydown", focusNewTask);
  }, [token, project, openTaskId, showNewProject, showNewEpic, showTrelloImport, showBitrixImport]);

  const loadProjects = useCallback(async () => {
    if (!token) return;
    const data: Project[] = (await api("/api/projects", {}, token)) ?? [];
    setProjects(data);
    const saved = localStorage.getItem("akhflow.project");
    if (data.length > 0) {
      setProject((p) => {
        if (p && data.some((x) => x.slug === p)) return p;
        if (saved && data.some((x) => x.slug === saved)) return saved;
        return data[0].slug;
      });
    }
  }, [token]);

  const loadTasks = useCallback(async () => {
    if (!token || !project) return;
    const data: Task[] = (await api(`/api/projects/${project}/tasks`, {}, token)) ?? [];
    setTasks(data);
  }, [token, project]);

  const fetchDeadlineTasks = useCallback(async (signal: AbortSignal): Promise<Task[]> => {
    return (await api(`/api/projects/${project}/tasks`, { signal }, token)) ?? [];
  }, [token, project]);

  const loadDashboard = useCallback(async (signal: AbortSignal) => {
    const [projectTasks, projectMembers] = await Promise.all([
      api(`/api/projects/${project}/tasks`, { signal }, token),
      api(`/api/projects/${project}/members`, { signal }, token),
    ]);
    return { tasks: projectTasks ?? [], members: projectMembers ?? [] };
  }, [token, project]);

  const loadChats = useCallback(async () => {
    if (!token || !project) return;
    const data: ChatSession[] = (await api(`/api/projects/${project}/chats`, {}, token)) ?? [];
    setChats(data);
    setActiveChat((prev) => {
      if (prev && data.some((c) => c.id === prev)) return prev;
      return data.length > 0 ? data[0].id : null;
    });
  }, [token, project]);

  const loadMessages = useCallback(
    async (chatId: number) => {
      const data: ChatMessage[] = (await api(`/api/chats/${chatId}/messages`, {}, token)) ?? [];
      setMessages(data);
    },
    [token],
  );

  useEffect(() => {
    loadChats();
  }, [loadChats]);

  useEffect(() => {
    if (activeChat) loadMessages(activeChat);
    else setMessages([]);
  }, [activeChat, loadMessages]);

  useEffect(() => {
    loadProjects();
  }, [loadProjects]);

  const loadEpics = useCallback(async () => {
    if (!token || !project) return;
    const data: Epic[] = (await api(`/api/projects/${project}/epics`, {}, token)) ?? [];
    setEpics(data);
  }, [token, project]);

  useEffect(() => {
    loadEpics();
  }, [loadEpics]);

  const loadMembers = useCallback(async () => {
    if (!token || !project) return;
    const data: Member[] = (await api(`/api/projects/${project}/members`, {}, token)) ?? [];
    setMembers(data);
  }, [token, project]);

  useEffect(() => {
    loadMembers();
  }, [loadMembers]);

  const visibleTasks = tasks.filter((t) =>
    epicFilter === "all" ? true : epicFilter === "none" ? !t.epic_id : t.epic_id === Number(epicFilter),
  );

  useEffect(() => {
    loadTasks();
  }, [loadTasks, project]);

  if (!token) return <Login onLogin={saveToken} />;

  const move = async (id: number, dir: number) => {
    const t = tasks.find((x) => x.id === id);
    if (!t) return;
    const i = COLUMNS.findIndex((c) => c.id === t.status);
    const target = COLUMNS[i + dir];
    if (!target) return;
    await api(`/api/tasks/${id}`, { method: "PATCH", body: JSON.stringify({ status: target.id }) }, token);
    await loadTasks();
  };

  const createProject = async () => {
    if (!newProjectName.trim()) return;
    const data: Project = await api("/api/projects", {
      method: "POST",
      body: JSON.stringify({ name: newProjectName.trim() }),
    }, token);
    setShowNewProject(false);
    setNewProjectName("");
    await loadProjects();
    setProject(data.slug);
    localStorage.setItem("akhflow.project", data.slug);
  };

  const createEpic = async () => {
    if (!newEpicTitle.trim()) return;
    await api(`/api/projects/${project}/epics`, {
      method: "POST",
      body: JSON.stringify({ title: newEpicTitle.trim() }),
    }, token);
    setShowNewEpic(false);
    setNewEpicTitle("");
    loadEpics();
  };

  const importTrello = async () => {
    if (!trelloBoard.trim() || trelloBusy) return;
    setTrelloBusy(true);
    setTrelloResult("");
    try {
      const result = await api(`/api/projects/${project}/imports/trello`, {
        method: "POST",
        body: JSON.stringify({ board_id: trelloBoard.trim() }),
      }, token);
      setTrelloResult(result.message);
      await Promise.all([loadTasks(), loadEpics()]);
    } catch (error: unknown) {
      setTrelloResult(error instanceof Error ? error.message : "Не удалось импортировать доску");
    } finally {
      setTrelloBusy(false);
    }
  };

  const patchTaskEpic = async (taskId: number, epicId: number | null) => {
    await api(`/api/tasks/${taskId}`, {
      method: "PATCH",
      body: JSON.stringify({ epic_id: epicId }),
    }, token);
    loadTasks();
  };

  const createTask = async () => {
    if (!newTitle.trim()) return;
    await api(`/api/projects/${project}/tasks`, {
      method: "POST",
      body: JSON.stringify({ title: newTitle, status: "backlog" }),
    }, token);
    setNewTitle("");
    loadTasks();
  };

  const importBitrixTasks = async () => {
    if (!project || bitrixBusy) return;
    const groupId = bitrixGroup.trim() ? Number(bitrixGroup) : 0;
    if (!Number.isSafeInteger(groupId) || groupId < 0) {
      setBitrixMessage("Укажи корректный номер группы Bitrix24");
      return;
    }
    setBitrixBusy(true);
    setBitrixMessage("");
    try {
      const result = await api(`/api/projects/${project}/imports/bitrix24`, {
        method: "POST",
        body: JSON.stringify({ group_id: groupId }),
      }, token);
      setBitrixMessage(result.message);
      await loadTasks();
    } catch {
      setBitrixMessage("Не удалось импортировать задачи. Проверь настройки подключения и повтори попытку.");
    } finally {
      setBitrixBusy(false);
    }
  };

  const newChat = async () => {
    if (!project) return;
    const data = await api(`/api/projects/${project}/chats`, { method: "POST", body: "{}" }, token);
    await loadChats();
    setActiveChat(data.id);
  };

  const askAgent = async () => {
    if (!input.trim() || busy || !activeChat) return;
    const text = input.trim();
    setInput("");
    setBusy(true);
    const tempId = Date.now();
    setMessages((m) => [...m, { id: tempId, role: "user", content: text }]);
    try {
      const data = await api(`/api/chats/${activeChat}/messages`, {
        method: "POST",
        body: JSON.stringify({ message: text }),
      }, token);
      setMessages((m) => [
        ...m,
        { id: tempId + 1, role: "agent", content: data.reply, tool_calls: data.tool_calls },
      ]);
    } catch (e: any) {
      setMessages((m) => [...m, { id: tempId + 1, role: "agent", content: `Ошибка: ${e.message}` }]);
    }
    setBusy(false);
    loadTasks();
    loadChats();
  };

  return (
    <div className="min-h-screen bg-background">
      <header className="border-b">
        <div className="max-w-7xl mx-auto px-4 min-h-14 flex flex-wrap items-center gap-4 py-2">
          <span className="font-semibold">Akhflow-AI</span>
          <select
            className="h-8 rounded-md border bg-transparent px-2 text-sm"
            value={project}
            onChange={(e) => {
              setProject(e.target.value);
              localStorage.setItem("akhflow.project", e.target.value);
            }}
          >
            {projects.map((p) => (
              <option key={p.slug} value={p.slug}>{p.name}</option>
            ))}
          </select>
          {showNewProject ? (
            <form className="flex gap-1" onSubmit={(e) => { e.preventDefault(); createProject(); }}>
              <Input
                className="h-8 w-44"
                placeholder="Название проекта"
                value={newProjectName}
                onChange={(e) => setNewProjectName(e.target.value)}
                autoFocus
              />
              <Button size="sm" type="submit">Создать</Button>
              <Button size="sm" variant="ghost" onClick={() => setShowNewProject(false)}>Отмена</Button>
            </form>
          ) : (
            <Button size="sm" variant="outline" onClick={() => setShowNewProject(true)}>
              <Plus /> Проект
            </Button>
          )}
          {project && <AkhcheckImport project={project} token={token} onImported={loadTasks} />}
          <select
            className="h-8 rounded-md border bg-transparent px-2 text-sm"
            value={epicFilter}
            onChange={(e) => setEpicFilter(e.target.value)}
          >
            <option value="all">Все эпики</option>
            <option value="none">Без эпика</option>
            {epics.map((e) => (
              <option key={e.id} value={e.id}>{e.title}</option>
            ))}
          </select>
          {showNewEpic ? (
            <form className="flex gap-1" onSubmit={(e) => { e.preventDefault(); createEpic(); }}>
              <Input
                className="h-8 w-44"
                placeholder="Название эпика"
                value={newEpicTitle}
                onChange={(e) => setNewEpicTitle(e.target.value)}
                autoFocus
              />
              <Button size="sm" type="submit">Создать</Button>
              <Button size="sm" variant="ghost" onClick={() => setShowNewEpic(false)}>Отмена</Button>
            </form>
          ) : (
            <Button size="sm" variant="outline" onClick={() => setShowNewEpic(true)}>
              <Layers /> Эпик
            </Button>
          )}
          {showTrelloImport ? (
            <form className="flex flex-wrap items-center gap-1" onSubmit={(e) => { e.preventDefault(); importTrello(); }}>
              <Input className="h-8 w-44" aria-label="ID доски Trello" placeholder="ID доски Trello" value={trelloBoard} onChange={(e) => setTrelloBoard(e.target.value)} />
              <Button size="sm" type="submit" disabled={trelloBusy || !trelloBoard.trim()}>{trelloBusy ? "Импортируем" : "Импортировать"}</Button>
              <Button size="sm" variant="ghost" type="button" onClick={() => { setShowTrelloImport(false); setTrelloResult(""); }}>Закрыть</Button>
              {trelloResult && <span role="status" className="text-sm">{trelloResult}</span>}
            </form>
          ) : (
            <Button size="sm" variant="outline" disabled={!project} onClick={() => setShowTrelloImport(true)}>Импорт Trello</Button>
          )}
          <div className="flex gap-1">
            <Button variant={view === "kanban" ? "default" : "ghost"} size="sm" onClick={() => setView("kanban")}>
              <KanbanSquare /> Канбан
            </Button>
            <Button variant={view === "list" ? "default" : "ghost"} size="sm" onClick={() => setView("list")}>
              <ListTodo /> Список
            </Button>
            <Button variant={view === "dashboard" ? "default" : "ghost"} size="sm" onClick={() => setView("dashboard")}>
              <ChartNoAxesCombined /> Сводка
            </Button>
            <Button variant={view === "agent" ? "default" : "ghost"} size="sm" onClick={() => setView("agent")}>
              <MessageSquare /> Агент
            </Button>
          </div>
          <div className="ml-auto flex items-center gap-2">
            <EmailNotifications token={token} request={api} />
            <Button variant="ghost" size="sm" onClick={() => { saveToken(null); }}>Выйти</Button>
          </div>
        </div>
      </header>

      <main className="max-w-7xl mx-auto px-4 py-6 space-y-4">
        {project && (
          <div className="space-y-2">
            <Button size="sm" variant="outline" onClick={() => setShowBitrixImport(!showBitrixImport)}>
              Импорт из Bitrix24
            </Button>
            {showBitrixImport && (
              <form className="flex flex-wrap items-center gap-2" onSubmit={(e) => { e.preventDefault(); importBitrixTasks(); }}>
                <label htmlFor="bitrix-group" className="text-sm">Номер группы (необязательно)</label>
                <Input id="bitrix-group" className="w-40" type="number" min="1" step="1" value={bitrixGroup} onChange={(e) => setBitrixGroup(e.target.value)} />
                <Button size="sm" type="submit" disabled={bitrixBusy}>{bitrixBusy ? "Импортируем" : "Импортировать"}</Button>
              </form>
            )}
            {bitrixMessage && <p role="status" className="text-sm">{bitrixMessage}</p>}
          </div>
        )}
        {project && (
          <DeadlineReminders
            key={`${token}:${project}`}
            project={project}
            fetchTasks={fetchDeadlineTasks}
            onOpen={setOpenTaskId}
          />
        )}
        <div className="flex gap-2">
          <Input
            ref={newTaskInput}
            aria-keyshortcuts="Alt+N"
            className="max-w-sm"
            placeholder="Новая задача..."
            value={newTitle}
            onChange={(e) => setNewTitle(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && createTask()}
          />
          <Button size="sm" onClick={createTask}><Plus /> Добавить</Button>
        </div>
        <p className="text-sm text-muted-foreground">Alt+N: перейти к созданию задачи.</p>

        {view === "kanban" && <Kanban tasks={visibleTasks} epics={epics} onOpen={setOpenTaskId} onMove={move} />}

        {view === "dashboard" && (
          project
            ? <ProjectDashboard key={project} load={loadDashboard} revision={tasks} />
            : <p className="text-sm text-muted-foreground">Создай проект, чтобы увидеть сводку.</p>
        )}

        {view === "list" && (
          <Card>
            <CardContent className="p-0">
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b text-left text-muted-foreground">
                    <th className="p-3 font-medium">ID</th>
                    <th className="p-3 font-medium">Задача</th>
                    <th className="p-3 font-medium">Статус</th>
                    <th className="p-3 font-medium">Приоритет</th>
                    <th className="p-3 font-medium">Эпик</th>
                    <th className="p-3 font-medium">Зависит от</th>
                  </tr>
                </thead>
                <tbody>
                  {visibleTasks.map((t) => (
                    <tr key={t.id} className="border-b last:border-0">
                      <td className="p-3 text-muted-foreground">{t.id}</td>
                      <td className="p-3">
                        <button className="hover:underline" onClick={() => setOpenTaskId(t.id)}>{t.title}</button>
                      </td>
                      <td className="p-3">{t.status}</td>
                      <td className="p-3">{t.priority}</td>
                      <td className="p-3">
                        <select
                          className="max-w-[180px] rounded border bg-transparent px-1 py-0.5 text-xs"
                          value={t.epic_id ?? ""}
                          onChange={(e) => patchTaskEpic(t.id, e.target.value ? Number(e.target.value) : null)}
                        >
                          <option value="">—</option>
                          {epics.map((e) => (
                            <option key={e.id} value={e.id}>{e.title}</option>
                          ))}
                        </select>
                      </td>
                      <td className="p-3 text-muted-foreground">
                        {t.depends_on?.length
                          ? t.depends_on.map((id) => `#${id}`).join(", ")
                          : "—"}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </CardContent>
          </Card>
        )}

        {view === "agent" && (
          <div className="grid grid-cols-[240px_1fr] gap-4">
            <Card>
              <CardHeader className="flex flex-row items-center justify-between pb-2">
                <CardTitle className="text-base">Чаты</CardTitle>
                <Button size="sm" variant="outline" onClick={newChat}><Plus /></Button>
              </CardHeader>
              <CardContent className="space-y-1 p-3">
                {chats.length === 0 && <p className="text-sm text-muted-foreground">Нет чатов</p>}
                {chats.map((c) => (
                  <button
                    key={c.id}
                    onClick={() => setActiveChat(c.id)}
                    className={`w-full truncate rounded-md px-2 py-1.5 text-left text-sm ${c.id === activeChat ? "bg-accent font-medium" : "hover:bg-muted"}`}
                  >
                    {c.title || `Чат #${c.id}`}
                  </button>
                ))}
              </CardContent>
            </Card>
            <Card>
              <CardHeader>
                <CardTitle>AI-агент — {project}</CardTitle>
              </CardHeader>
              <CardContent className="max-h-[55vh] space-y-3 overflow-y-auto">
                {!activeChat && (
                  <p className="text-sm text-muted-foreground">
                    Создай чат кнопкой «+» слева и спроси агента о задачах проекта.
                  </p>
                )}
                {activeChat && messages.length === 0 && (
                  <p className="text-sm text-muted-foreground">
                    Попробуй: «Перемести задачу 3 в done», «Создай задачу Подготовить демо», «Что в backlog?»
                  </p>
                )}
                {messages.map((m) => (
                  <div key={m.id} className={`space-y-1 ${m.role === "user" ? "text-right" : ""}`}>
                    {m.role === "user" ? (
                      <p className="inline-block rounded-lg bg-primary px-3 py-2 text-sm text-primary-foreground">
                        {m.content}
                      </p>
                    ) : (
                      <div className="prose prose-sm max-w-none rounded-lg bg-muted px-3 py-2 dark:prose-invert">
                        <ReactMarkdown remarkPlugins={[remarkGfm]}>{m.content}</ReactMarkdown>
                      </div>
                    )}
                    {m.tool_calls?.map((tc, j) => (
                      <p key={j} className="break-all text-xs text-muted-foreground font-mono">
                        {tc.tool}({typeof tc.args === "string" ? tc.args : JSON.stringify(tc.args)}) → {briefToolResult(tc.result)}
                      </p>
                    ))}
                  </div>
                ))}
              </CardContent>
              <CardContent className="pt-0">
                {activeChat && (
                  <div className="flex gap-2">
                    <Input
                      value={input}
                      onChange={(e) => setInput(e.target.value)}
                      onKeyDown={(e) => e.key === "Enter" && askAgent()}
                      placeholder="Скажи агенту, что сделать..."
                    />
                    <Button onClick={askAgent} disabled={busy}><Send /></Button>
                  </div>
                )}
              </CardContent>
            </Card>
          </div>
        )}
      </main>

      {openTaskId && (
        <TaskDetail
          taskId={openTaskId}
          epics={epics}
          members={members}
          tasks={tasks}
          onOpen={setOpenTaskId}
          onClose={() => setOpenTaskId(null)}
          onChanged={loadTasks}
        />
      )}
    </div>
  );
}
