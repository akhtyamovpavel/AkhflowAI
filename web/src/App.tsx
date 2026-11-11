import { useCallback, useEffect, useRef, useState, useSyncExternalStore } from "react";
import type { KeyboardEvent as ReactKeyboardEvent } from "react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { DeadlineReminders } from "@/components/DeadlineReminders";
import { reminderUserId } from "@/lib/deadlines";
import { EmailNotifications } from "@/components/EmailNotifications";
import { ProjectDashboard } from "@/components/ProjectDashboard";
import { AgentUsage } from "@/components/AgentUsage";
import type { AgentUsageData } from "@/components/AgentUsage";
import { ProjectMembers } from "@/components/ProjectMembers";
import { ThemeToggle } from "@/components/ThemeToggle";
import { QuickSearch } from "@/components/QuickSearch";
import { TaskAttachments } from "@/components/TaskAttachments";
import { AssigneeSuggestion } from "@/components/AssigneeSuggestion";
import { readSharedTask, telegramShareURL } from "@/lib/taskShare";
import type { SharedTask } from "@/lib/taskShare";
import { subscribeProjectEvents } from "@/lib/projectEvents";
import { useAgentMessageGuard } from "@/lib/agentMessageGuard";
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
  role: string;
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
  useEffect(() => {
    const syncToken = (event: StorageEvent) => {
      if (event.storageArea === localStorage && (event.key === "akhflow.token" || event.key === null)) {
        setToken(localStorage.getItem("akhflow.token"));
      }
    };
    window.addEventListener("storage", syncToken);
    setToken(localStorage.getItem("akhflow.token"));
    return () => window.removeEventListener("storage", syncToken);
  }, []);
  const save = (t: string | null) => {
    if (t) localStorage.setItem("akhflow.token", t);
    else localStorage.removeItem("akhflow.token");
    setToken(t);
  };
  return [token, save] as const;
}

type ApiRequest = (path: string, opts?: RequestInit, token?: string | null, expectedStatus?: number) => Promise<any>;

export async function api(path: string, opts: RequestInit = {}, token?: string | null, onForbidden?: () => void, expectedStatus?: number) {
  const res = await fetch(API + path, {
    ...opts,
    headers: {
      "Content-Type": "application/json",
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
      ...(opts.headers || {}),
    },
  });
  if (!res.ok) {
    if (res.status === 401 && token && !opts.signal?.aborted && localStorage.getItem("akhflow.token") === token) {
      localStorage.removeItem("akhflow.token");
      window.location.reload();
    }
    if (res.status === 403 && token && localStorage.getItem("akhflow.token") === token &&
        !opts.signal?.aborted && opts.method && !["GET", "HEAD"].includes(opts.method.toUpperCase())) {
      onForbidden?.();
    }
    const body = await res.text();
    throw new Error(`${res.status}: ${body}`);
  }
  if (expectedStatus !== undefined && res.status !== expectedStatus) throw new Error("unexpected response status");
  return res.json();
}

function isToolCallList(value: unknown): value is NonNullable<ChatMessage["tool_calls"]> {
  return Array.isArray(value) && value.every((call: unknown) => {
    if (!call || typeof call !== "object" || Array.isArray(call)) return false;
    const log = call as Record<string, unknown>;
    return typeof log.tool === "string" && typeof log.args === "string" && typeof log.result === "string";
  });
}

function isCompletedAgentResponse(value: unknown): value is { reply: string; tool_calls: NonNullable<ChatMessage["tool_calls"]> | null; iterations: number } {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const result = value as Record<string, unknown>;
  if ("error" in result || typeof result.reply !== "string" || !result.reply.trim() ||
      !Number.isSafeInteger(result.iterations) || (result.iterations as number) < 1) return false;
  return result.tool_calls === null || isToolCallList(result.tool_calls);
}

function isChatHistory(value: unknown): value is ChatMessage[] {
  return Array.isArray(value) && value.every((entry: unknown) => {
    if (!entry || typeof entry !== "object" || Array.isArray(entry)) return false;
    const message = entry as Record<string, unknown>;
    return Number.isSafeInteger(message.id) && (message.id as number) > 0 &&
      typeof message.role === "string" && typeof message.content === "string" &&
      (message.tool_calls === undefined || message.tool_calls === null || isToolCallList(message.tool_calls));
  });
}

function AkhcheckImport({ project, token, onImported, request }: { project: string; token: string | null; onImported: () => Promise<void>; request: ApiRequest }) {
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
      const result = await request(`/api/projects/${encodeURIComponent(project)}/imports/akhcheck`, {
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
  const [registering, setRegistering] = useState(false);
  const registrationPending = useRef(false);
  const loginRequest = useRef(0);
  useEffect(() => {
    const invalidateLogin = (event: StorageEvent) => {
      if (event.storageArea === localStorage && (event.key === "akhflow.token" || event.key === null)) {
        loginRequest.current++;
      }
    };
    window.addEventListener("storage", invalidateLogin);
    return () => {
      loginRequest.current++;
      window.removeEventListener("storage", invalidateLogin);
    };
  }, []);
  const submit = async () => {
    const isRegistration = mode === "register";
    if (isRegistration && registrationPending.current) return;
    if (isRegistration) {
      registrationPending.current = true;
      setRegistering(true);
    }
    const request = ++loginRequest.current;
    const initialToken = localStorage.getItem("akhflow.token");
    const isCurrent = () => request === loginRequest.current && localStorage.getItem("akhflow.token") === initialToken;
    setErr("");
    try {
      const path = mode === "login" ? "/api/auth/login" : "/api/auth/register";
      const body: any = { email, password };
      if (mode === "register") body.display_name = name || email;
      const data = await api(path, { method: "POST", body: JSON.stringify(body) });
      if (isCurrent()) {
        onLogin(data.token);
      }
    } catch (e: any) {
      if (isCurrent()) {
        const message = e.message.startsWith("503:")
          ? "Сервис временно недоступен. Попробуйте позже."
          : e.message;
        setErr(message);
      }
    } finally {
      if (isRegistration) {
        registrationPending.current = false;
        setRegistering(false);
      }
    }
  };
  return (
    <div className="min-h-screen flex items-center justify-center bg-background">
      <Card className="w-full max-w-sm">
        <CardHeader>
          <div className="flex items-center justify-between gap-2">
            <CardTitle>Akhflow-AI</CardTitle>
            <ThemeToggle />
          </div>
        </CardHeader>
        <CardContent className="space-y-3">
          {mode === "register" && (
            <Input placeholder="Имя" value={name} disabled={registering} onChange={(e) => setName(e.target.value)} />
          )}
          <Input type="email" placeholder="Email" value={email} disabled={registering} onChange={(e) => setEmail(e.target.value)} />
          <Input type="password" placeholder="Пароль" value={password} disabled={registering} onChange={(e) => setPassword(e.target.value)} />
          {err && <p className="text-sm text-destructive">{err}</p>}
          <Button className="w-full" onClick={submit} disabled={registering}>
            {mode === "login" ? "Войти" : "Создать аккаунт"}
          </Button>
          <Button variant="ghost" className="w-full" disabled={registering} onClick={() => setMode(mode === "login" ? "register" : "login")}>
            {mode === "login" ? "Нет аккаунта? Зарегистрироваться" : "Есть аккаунт? Войти"}
          </Button>
        </CardContent>
      </Card>
    </div>
  );
}

function Kanban({ tasks, allTasks, epics, selected, selectionDisabled, onSelect, onOpen, onMove }: {
  tasks: Task[];
  allTasks: Task[];
  epics: Epic[];
  selected: Set<number>;
  selectionDisabled: boolean;
  onSelect: (id: number, checked: boolean) => void;
  onOpen: (id: number) => void;
  onMove: (id: number, dir: number) => Promise<void>;
}) {
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
    if (!target || moving.current || selectionDisabled) return;
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
      const dep = allTasks.find((x) => x.id === id);
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
                    <input
                      type="checkbox"
                      className="mr-2"
                      aria-label={`Выбрать задачу ${t.title} #${t.id}`}
                      checked={selected.has(t.id)}
                      disabled={selectionDisabled}
                      onChange={(event) => onSelect(t.id, event.target.checked)}
                    />
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
                      <Button variant="outline" size="sm" aria-label={`Переместить ${t.title} влево`} disabled={selectionDisabled || i === 0} onClick={() => void moveTask(t, -1, false)}>
                        ←
                      </Button>
                      <Button variant="outline" size="sm" aria-label={`Переместить ${t.title} вправо`} disabled={selectionDisabled || i === COLUMNS.length - 1} onClick={() => void moveTask(t, 1, false)}>
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

const taskTitleDrafts = new Map<string, string>();
const taskTitleWrites = new Map<string, Promise<unknown>>();
type CommentDraft = {
  value: string;
  revision: number;
  generation: number;
  pending: boolean;
  error: string;
  listeners: Set<(refresh: boolean) => void>;
};
const taskCommentDrafts = new Map<string, CommentDraft>();

function commentDraftFor(key: string): CommentDraft {
  let draft = taskCommentDrafts.get(key);
  if (!draft) {
    draft = { value: "", revision: 0, generation: 0, pending: false, error: "", listeners: new Set() };
    taskCommentDrafts.set(key, draft);
  }
  return draft;
}

function notifyCommentDraft(draft: CommentDraft, refresh = false) {
  for (const listener of draft.listeners) {
    listener(refresh);
  }
}

type DependencyRemoval = {
  sourceId: number;
  depId: number;
  confirmed: boolean;
};

function TaskDetail({ taskId, project, epics, members, membersLoaded, membersError, membersLoading, onRetryMembers, tasks, recommendationReady, onOpen, onClose, onChanged, request }: {
  taskId: number;
  project: string;
  epics: Epic[];
  members: Member[];
  membersLoaded: boolean;
  membersError: boolean;
  membersLoading: boolean;
  onRetryMembers: () => void;
  tasks: Task[];
  recommendationReady: boolean;
  onOpen: (id: number) => void;
  onClose: () => void;
  onChanged: () => void | Promise<void>;
  request: ApiRequest;
}) {
  const token = localStorage.getItem("akhflow.token");
  const draftKey = JSON.stringify([token, project, taskId]);
  const initialTitle = taskTitleDrafts.get(draftKey) ?? "";
  const initialTitleDirty = taskTitleDrafts.has(draftKey);
  const [task, setTask] = useState<Task | null>(null);
  const [title, setTitle] = useState(initialTitle);
  const [titleDirty, setTitleDirty] = useState(initialTitleDirty);
  const [titleError, setTitleError] = useState("");
  const [savingTitle, setSavingTitle] = useState(false);
  const titleDraft = useRef({ value: initialTitle, revision: 0, dirty: initialTitleDirty });
  const titleSavePending = useRef(false);
  const [comments, setComments] = useState<Comment[]>([]);
  const [deps, setDeps] = useState<Task[]>([]);
  const [blocks, setBlocks] = useState<Task[]>([]);
  const [history, setHistory] = useState<AuditEntry[]>([]);
  const [desc, setDesc] = useState("");
  const description = useRef({ value: "", saved: "", version: 0 });
  const [savingDescription, setSavingDescription] = useState(false);
  const descriptionSaving = useRef(false);
  const [descriptionError, setDescriptionError] = useState("");
  const [descriptionRefreshFailed, setDescriptionRefreshFailed] = useState(false);
  const commentDraft = commentDraftFor(draftKey);
  const [comment, setComment] = useState(commentDraft.value);
  const [sendingComment, setSendingComment] = useState(commentDraft.pending);
  const [commentError, setCommentError] = useState(commentDraft.error);
  const [newDep, setNewDep] = useState("");
  const [dependencyAddResults, setDependencyAddResults] = useState<Record<string, { message: string; checked?: boolean }>>({});
  const dependencySelectionRevision = useRef(0);
  const dependencyMutationRevision = useRef(0);
  const dependencyAddsPending = useRef(new Set<string>());
  const [pendingDependencyCount, setPendingDependencyCount] = useState(0);
  const dependencyRefreshPending = useRef(false);
  const [refreshingDependencies, setRefreshingDependencies] = useState(false);
  const addingDependency = pendingDependencyCount > 0 || refreshingDependencies;
  const [dependencyError, setDependencyError] = useState("");
  const [dependencyRemovalStatus, setDependencyRemovalStatus] = useState("");
  const [dependencyRemoval, setDependencyRemoval] = useState<DependencyRemoval | null>(null);
  const [removingDependency, setRemovingDependency] = useState(false);
  const dependencyRemovalPending = useRef(false);
  const detailRequest = useRef(0);
  const detailController = useRef<AbortController | null>(null);
  const initialLoadPending = useRef(false);
  const taskLoaded = useRef(false);
  const [initialLoading, setInitialLoading] = useState(true);
  const [initialError, setInitialError] = useState(false);

  const load = useCallback(async () => {
    if (!token) return;
    const requestVersion = ++detailRequest.current;
    detailController.current?.abort();
    const controller = new AbortController();
    detailController.current = controller;
    if (!taskLoaded.current) {
      initialLoadPending.current = true;
      setInitialLoading(true);
      setInitialError(false);
    }
    const titleRevision = titleDraft.current.revision;
    const version = description.current.version;
    let data;
    try {
      data = await request(`/api/tasks/${taskId}`, { signal: controller.signal }, token);
    } catch (error) {
      if (requestVersion !== detailRequest.current) return;
      if (!taskLoaded.current) {
        setInitialError(true);
      }
      throw error;
    } finally {
      if (requestVersion === detailRequest.current) {
        initialLoadPending.current = false;
        setInitialLoading(false);
      }
    }
    if (requestVersion !== detailRequest.current) return;
    taskLoaded.current = true;
    setTask(data.task);
    if (!titleDraft.current.dirty && titleDraft.current.revision === titleRevision) {
      titleDraft.current.value = data.task.title;
      setTitle(data.task.title);
    }
    setComments(data.comments ?? []);
    setDeps(data.dependencies ?? []);
    setBlocks(data.blocks ?? []);
    setHistory(data.history ?? []);
    const draft = description.current;
    if (draft.version === version && draft.value === draft.saved) {
      const value = data.task.description ?? "";
      draft.value = value;
      draft.saved = value;
      setDesc(value);
    }
    return data;
  }, [taskId, token, request]);

  const loadInitial = useCallback(async () => {
    if (initialLoadPending.current) return;
    try {
      await load();
    } catch {
      // The load handler exposes an error for the current request.
    }
  }, [load]);

  useEffect(() => {
    void loadInitial();
    return () => {
      detailRequest.current++;
      detailController.current?.abort();
      initialLoadPending.current = false;
    };
  }, [loadInitial]);

  useEffect(() => {
    const update = (refresh: boolean) => {
      setComment(commentDraft.value);
      setSendingComment(commentDraft.pending);
      setCommentError(commentDraft.error);
      if (refresh) {
        const generation = commentDraft.generation;
        void load().catch(() => {
          if (commentDraft.generation === generation) {
            commentDraft.error = "Комментарий отправлен, но обновить список не удалось.";
            notifyCommentDraft(commentDraft);
          }
        });
      }
    };
    commentDraft.listeners.add(update);
    update(false);
    return () => { commentDraft.listeners.delete(update); };
  }, [commentDraft, load]);

  useEffect(() => {
    if (newDep && deps.some((dependency) => dependency.id === Number(newDep))) {
      setNewDep("");
    }
  }, [deps, newDep]);

  if (!task) {
    return (
      <div className="fixed inset-0 z-50" onClick={onClose}>
        <div className="absolute inset-0 bg-black/30" />
        <div
          className="absolute inset-y-0 right-0 w-full max-w-md overflow-y-auto border-l bg-background shadow-xl"
          onClick={(event) => event.stopPropagation()}
        >
          <div className="flex items-center justify-between border-b p-4">
            <span className="text-sm text-muted-foreground">Задача #{taskId}</span>
            <Button size="sm" variant="ghost" aria-label="Закрыть карточку задачи" onClick={onClose}>
              <X />
            </Button>
          </div>
          <div className="space-y-3 p-4">
            {initialLoading && <p role="status">Загружаем карточку задачи...</p>}
            {initialError && (
              <div role="alert" className="space-y-2 text-sm text-destructive">
                <p>Не удалось загрузить карточку задачи. Попробуй ещё раз.</p>
                <Button size="sm" variant="outline" disabled={initialLoading} onClick={() => void loadInitial()}>
                  Повторить загрузку
                </Button>
              </div>
            )}
          </div>
        </div>
      </div>
    );
  }

  const patch = async (body: unknown) => {
    await request(`/api/tasks/${taskId}`, { method: "PATCH", body: JSON.stringify(body) }, token);
    await load();
    onChanged();
  };

  const saveTitle = async () => {
    if (titleSavePending.current || !titleDraft.current.dirty) return;
    const { value, revision } = titleDraft.current;
    titleSavePending.current = true;
    setSavingTitle(true);
    setTitleError("");
    const previousWrite = taskTitleWrites.get(draftKey) ?? Promise.resolve();
    const write = previousWrite.catch(() => {}).then(() => request(`/api/tasks/${taskId}`, { method: "PATCH", body: JSON.stringify({ title: value }) }, token));
    taskTitleWrites.set(draftKey, write);
    try {
      await write;
      if (taskTitleDrafts.get(draftKey) === value) {
        taskTitleDrafts.delete(draftKey);
      }
      if (titleDraft.current.revision === revision) {
        titleDraft.current.dirty = false;
        setTitleDirty(false);
      }
      titleDraft.current.revision++;
      try {
        await load();
        await onChanged();
      } catch {
        setTitleError("Не удалось обновить карточку после сохранения заголовка. Введённый текст остался в поле.");
      }
    } catch {
      setTitleError("Не удалось сохранить заголовок. Введённый текст остался в поле. Попробуй ещё раз.");
    } finally {
      if (taskTitleWrites.get(draftKey) === write) {
        taskTitleWrites.delete(draftKey);
      }
      titleSavePending.current = false;
      setSavingTitle(false);
    }
  };

  const refreshDescriptionData = async () => {
    const updates = await Promise.allSettled([load(), Promise.resolve().then(onChanged)]);
    const failed = updates.some((result) => result.status === "rejected");
    setDescriptionRefreshFailed(failed);
    if (failed) {
      setDescriptionError("Описание сохранено, но обновить данные не удалось. Повтори обновление.");
    } else {
      setDescriptionError("");
    }
  };

  const retryDescriptionRefresh = async () => {
    if (descriptionSaving.current) return;
    descriptionSaving.current = true;
    setSavingDescription(true);
    try {
      await refreshDescriptionData();
    } finally {
      descriptionSaving.current = false;
      setSavingDescription(false);
    }
  };

  const saveDescription = async () => {
    if (descriptionSaving.current) return;
    const value = description.current.value;
    descriptionSaving.current = true;
    setSavingDescription(true);
    setDescriptionError("");
    setDescriptionRefreshFailed(false);
    try {
      await request(`/api/tasks/${taskId}`, { method: "PATCH", body: JSON.stringify({ description: value }) }, token);
      description.current.saved = value;
      description.current.version += 1;
      await refreshDescriptionData();
    } catch {
      setDescriptionError("Не удалось сохранить описание. Попробуй ещё раз.");
    } finally {
      descriptionSaving.current = false;
      setSavingDescription(false);
    }
  };

  const canLeave = () => {
    if (descriptionSaving.current || commentDraft.pending) return false;
    if (description.current.value !== description.current.saved) {
      return window.confirm("Описание не сохранено. Закрыть карточку и потерять изменения?");
    }
    return true;
  };

  const closeDetail = () => {
    if (titleDraft.current.dirty || titleSavePending.current) {
      setTitleError("Дождись сохранения заголовка или повтори попытку, прежде чем закрыть карточку.");
      return;
    }
    if (canLeave()) onClose();
  };

  const openDetail = (id: number) => {
    if (canLeave()) onOpen(id);
  };

  const addComment = async () => {
    if (commentDraft.pending || !commentDraft.value.trim()) return;
    const body = commentDraft.value.trim();
    const revision = commentDraft.revision;
    commentDraft.generation++;
    commentDraft.pending = true;
    commentDraft.error = "";
    notifyCommentDraft(commentDraft);
    try {
      await request(`/api/tasks/${taskId}/comments`, {
        method: "POST",
        body: JSON.stringify({ body }),
      }, token);
      if (commentDraft.revision === revision) {
        commentDraft.value = "";
      }
      commentDraft.pending = false;
      notifyCommentDraft(commentDraft, true);
    } catch {
      commentDraft.pending = false;
      commentDraft.error = "Не удалось подтвердить отправку. Проверь список комментариев перед повтором.";
      notifyCommentDraft(commentDraft);
    }
  };

  const setDependencyAddResult = (id: string, message: string, checked = false) => {
    setDependencyAddResults((results) => ({ ...results, [id]: { message, checked } }));
  };

  const clearDependencyAddResult = (id: string) => {
    setDependencyAddResults((results) => {
      const next = { ...results };
      delete next[id];
      return next;
    });
  };

  const refreshDependencyChoices = async (selected: string) => {
    if (dependencyAddsPending.current.size || dependencyRefreshPending.current) return;
    dependencyRefreshPending.current = true;
    setRefreshingDependencies(true);
    try {
      const data = await load();
      if (!data) {
        throw new Error("dependency refresh superseded");
      }
      await onChanged();
      const found = (data.dependencies ?? []).some((dependency: Task) => dependency.id === Number(selected));
      const message = found ? "Проверка завершена: зависимость добавлена." : "Проверка завершена: зависимость отсутствует. Можно повторить добавление.";
      setDependencyAddResult(selected, message, true);
    } catch {
      setDependencyAddResult(selected, "Не удалось обновить данные о связи. Попробуй ещё раз.");
    } finally {
      dependencyRefreshPending.current = false;
      setRefreshingDependencies(false);
    }
  };

  const finishDependencyAdd = (selected: string) => {
    dependencyAddsPending.current.delete(selected);
    setPendingDependencyCount(dependencyAddsPending.current.size);
  };

  const addDep = async () => {
    const selected = newDep;
    if (!selected || dependencyAddsPending.current.has(selected) || dependencyRefreshPending.current) return;
    const revision = dependencySelectionRevision.current;
    dependencyMutationRevision.current++;
    dependencyAddsPending.current.add(selected);
    setPendingDependencyCount(dependencyAddsPending.current.size);
    clearDependencyAddResult(selected);
    try {
      await request(`/api/tasks/${taskId}/dependencies`, {
        method: "POST",
        body: JSON.stringify({ depends_on: Number(selected) }),
      }, token);
    } catch (error) {
      const conflict = error instanceof Error && error.message.startsWith("409:");
      const message = conflict
        ? "Не удалось добавить зависимость: связь создаёт цикл. Выбери другую задачу или повтори после изменения связей."
        : "Не удалось подтвердить добавление зависимости. Обнови связи перед повторной отправкой.";
      setDependencyAddResult(selected, message);
      finishDependencyAdd(selected);
      return;
    }
    if (dependencySelectionRevision.current === revision) {
      setNewDep("");
    }
    try {
      await load();
      await onChanged();
    } catch {
      setDependencyAddResult(selected, "Зависимость добавлена, но обновить данные не удалось. Обнови связь.");
    } finally {
      finishDependencyAdd(selected);
    }
  };

  const refreshDependencyRemoval = async (target: DependencyRemoval) => {
    const mutationRevision = dependencyMutationRevision.current;
    const additionsWerePending = dependencyAddsPending.current.size > 0;
    const detailLoad = load();
    const requestVersion = detailRequest.current;
    const results = await Promise.allSettled([detailLoad, Promise.resolve().then(onChanged)]);
    const detail = results[0];
    if (detail.status === "rejected" || !detail.value || results[1].status === "rejected") {
      const message = target.confirmed
        ? "Связь удалена, но обновить данные не удалось. Повтори проверку."
        : "Не удалось обновить данные о связях. Проверь связи перед повторным удалением.";
      setDependencyError(message);
      return;
    }
    const changed = requestVersion !== detailRequest.current || mutationRevision !== dependencyMutationRevision.current;
    if (changed || additionsWerePending || dependencyAddsPending.current.size > 0) {
      setDependencyError("Данные карточки изменились во время проверки. Повтори проверку удаления связи.");
      return;
    }
    const data = detail.value;
    const links = target.sourceId === taskId ? data.dependencies : data.blocks;
    const linkedTask = target.sourceId === taskId ? target.depId : target.sourceId;
    const present = (links ?? []).some((dependency: Task) => dependency.id === linkedTask);
    const message = present
      ? "Проверка завершена: связь присутствует. Можно повторить удаление."
      : "Проверка завершена: связь отсутствует.";
    setDependencyRemovalStatus(message);
    setDependencyError("");
    setDependencyRemoval(null);
  };

  const checkDependencyRemoval = async () => {
    if (!dependencyRemoval || dependencyRemovalPending.current) return;
    dependencyRemovalPending.current = true;
    setRemovingDependency(true);
    try {
      await refreshDependencyRemoval(dependencyRemoval);
    } finally {
      dependencyRemovalPending.current = false;
      setRemovingDependency(false);
    }
  };

  const removeDep = async (sourceId: number, depId: number) => {
    if (dependencyRemovalPending.current || dependencyRemoval) return;
    const target = { sourceId, depId, confirmed: false };
    dependencyMutationRevision.current++;
    dependencyRemovalPending.current = true;
    setDependencyRemoval(target);
    setDependencyRemovalStatus("");
    setDependencyError("");
    setRemovingDependency(true);
    try {
      await request(`/api/tasks/${sourceId}/dependencies/${depId}`, { method: "DELETE" }, token);
    } catch {
      setDependencyError("Не удалось подтвердить удаление связи. Проверь связи перед повторным удалением.");
      dependencyRemovalPending.current = false;
      setRemovingDependency(false);
      return;
    }
    const confirmed = { ...target, confirmed: true };
    setDependencyRemoval(confirmed);
    try {
      await refreshDependencyRemoval(confirmed);
    } finally {
      dependencyRemovalPending.current = false;
      setRemovingDependency(false);
    }
  };

  const depRow = (d: Task, direction: "depends" | "blocks") => (
    <div key={d.id} className="flex items-center justify-between gap-2 rounded-md border p-2">
      <button className="text-left text-sm hover:underline" onClick={() => openDetail(d.id)}>
        #{d.id} {d.title}
      </button>
      <span className={`shrink-0 text-xs px-1.5 py-0.5 rounded ${d.status === "done" ? "bg-secondary text-secondary-foreground" : "bg-amber-500/10 text-amber-600"}`}>
        {d.status}
      </span>
      <Button
        size="sm"
        variant="ghost"
        aria-label={`Удалить связь с задачей #${d.id}: ${d.title}`}
        disabled={removingDependency || dependencyRemoval !== null}
        onClick={() => {
          if (direction === "depends") {
            void removeDep(taskId, d.id);
          } else {
            void removeDep(d.id, taskId);
          }
        }}
      >
        <X />
      </Button>
    </div>
  );

  const depOptions = tasks.filter(
    (t) => t.id !== taskId && !deps.some((d) => d.id === t.id),
  );

  const authorName = (id?: number | null) =>
    members.find((m) => m.id === id)?.display_name || (id ? `user #${id}` : "—");

  return (
    <div className="fixed inset-0 z-50" onClick={closeDetail}>
      <div className="absolute inset-0 bg-black/30" />
      <div
        className="absolute inset-y-0 right-0 w-full max-w-md overflow-y-auto border-l bg-background shadow-xl"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between border-b p-4">
          <span className="text-sm text-muted-foreground">Задача #{task.id}</span>
          <Button size="sm" variant="ghost" aria-label="Закрыть карточку задачи" onClick={closeDetail}>
            <X />
          </Button>
        </div>
        <div className="space-y-4 p-4">
          <Button variant="outline" size="sm" onClick={() => {
            const link = telegramShareURL(window.location, project, task.id, task.title);
            window.open(link, "_blank", "noopener,noreferrer");
          }}>
            Обсудить в Telegram
          </Button>
          <Input
            aria-label="Заголовок задачи"
            aria-invalid={!!titleError && titleDirty}
            value={title}
            onChange={(e) => {
              const value = e.target.value;
              taskTitleDrafts.set(draftKey, value);
              titleDraft.current = { value, revision: titleDraft.current.revision + 1, dirty: true };
              setTitle(value);
              setTitleDirty(true);
              setTitleError("");
            }}
            onBlur={() => void saveTitle()}
            onKeyDown={(e) => e.key === "Enter" && (e.target as HTMLInputElement).blur()}
            className="text-base font-medium"
          />
          {savingTitle && <p role="status" className="text-sm text-muted-foreground">Сохраняем заголовок...</p>}
          {titleError && <p role="alert" className="text-sm text-destructive">{titleError}</p>}
          {titleDirty && !savingTitle && (
            <Button size="sm" variant="outline" onClick={() => void saveTitle()}>
              {titleError ? "Повторить сохранение" : "Сохранить заголовок"}
            </Button>
          )}
          {titleDirty && !savingTitle && (
            <Button size="sm" variant="ghost" onPointerDown={(event) => event.preventDefault()} onClick={() => {
              taskTitleDrafts.delete(draftKey);
              titleDraft.current = { value: task.title, revision: titleDraft.current.revision + 1, dirty: false };
              setTitle(task.title);
              setTitleDirty(false);
              setTitleError("");
            }}>
              Отменить изменение заголовка
            </Button>
          )}
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
                disabled={!membersLoaded}
                onChange={(e) => patch({ assignee_id: e.target.value ? Number(e.target.value) : null })}
              >
                <option value="">—</option>
                {members.map((m) => <option key={m.id} value={m.id}>{m.display_name}</option>)}
              </select>
            </label>
            {membersError && (
              <div role="alert" className="col-span-2 space-y-2 text-sm">
                <p>Не удалось загрузить участников проекта. Попробуй ещё раз.</p>
                <Button variant="outline" size="sm" disabled={membersLoading} onClick={onRetryMembers}>
                  Повторить загрузку участников
                </Button>
              </div>
            )}
            <div className="col-span-2">
              <AssigneeSuggestion members={members} tasks={tasks} ready={recommendationReady} />
            </div>
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
              onChange={(e) => {
                description.current.value = e.target.value;
                setDesc(e.target.value);
              }}
              placeholder="Описание задачи..."
            />
            {descriptionError && <p role="alert" className="text-sm text-destructive">{descriptionError}</p>}
            {descriptionRefreshFailed && (
              <Button size="sm" variant="outline" disabled={savingDescription} onPointerDown={(event) => event.preventDefault()} onClick={retryDescriptionRefresh}>
                Повторить обновление
              </Button>
            )}
            <Button size="sm" variant="outline" disabled={savingDescription} onClick={saveDescription}>Сохранить</Button>
          </div>
          <div>
            <div className="mb-1 text-xs text-muted-foreground">Зависит от ({deps.length})</div>
            {dependencyError && (
              <div role="alert" className="text-sm text-destructive">
                {dependencyError}
                <Button size="sm" variant="ghost" disabled={removingDependency} onClick={() => void checkDependencyRemoval()}>Проверить удаление связи</Button>
              </div>
            )}
            {dependencyRemovalStatus && <p role="status" className="text-sm">{dependencyRemovalStatus}</p>}
            {Object.entries(dependencyAddResults).map(([id, result]) => (
              <div key={id} role={result.checked ? "status" : "alert"} className="text-sm">
                <p>Зависимость #{id}: {result.message}</p>
                {!result.checked && <Button size="sm" variant="ghost" disabled={addingDependency} onClick={() => void refreshDependencyChoices(id)}>Проверить связь #{id}</Button>}
              </div>
            ))}
            {addingDependency && <p role="status" className="text-sm text-muted-foreground">Обновляем зависимости...</p>}
            <div className="space-y-1">
              {deps.length === 0 && <p className="text-sm text-muted-foreground">Пока пусто</p>}
              {deps.map((dependency) => depRow(dependency, "depends"))}
            </div>
            {depOptions.length > 0 && (
              <div className="mt-2 flex gap-2">
                <select
                  className="min-w-0 flex-1 rounded-md border bg-transparent px-2 py-1.5 text-sm"
                  aria-label="Новая зависимость"
                  value={newDep}
                  onChange={(e) => {
                    dependencySelectionRevision.current++;
                    setNewDep(e.target.value);
                  }}
                >
                  <option value="">Выберите задачу...</option>
                  {depOptions.map((t) => (
                    <option key={t.id} value={t.id}>#{t.id} {t.title}</option>
                  ))}
                </select>
                <Button size="sm" onClick={() => void addDep()} disabled={!newDep || dependencyAddsPending.current.has(newDep) || refreshingDependencies}>Добавить</Button>
              </div>
            )}
          </div>
          <TaskAttachments taskId={taskId} token={token} apiBase={API} project={project} />
          {blocks.length > 0 && (
            <div>
              <div className="mb-1 text-xs text-muted-foreground">Блокирует ({blocks.length})</div>
              <div className="space-y-1">{blocks.map((dependency) => depRow(dependency, "blocks"))}</div>
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
                onChange={(e) => {
                  const value = e.target.value;
                  commentDraft.revision++;
                  commentDraft.value = value;
                  notifyCommentDraft(commentDraft);
                }}
                onKeyDown={(e) => {
                  if (e.key === "Enter" && !e.nativeEvent.isComposing) {
                    e.preventDefault();
                    void addComment();
                  }
                }}
                placeholder="Комментарий..."
              />
              <Button size="sm" aria-label="Отправить комментарий" disabled={sendingComment} onClick={addComment}><Send /></Button>
            </div>
            {commentError && <p role="alert" className="text-sm text-destructive">{commentError}</p>}
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
  if (!token) return <Login onLogin={saveToken} />;
  return <AccountWorkspace key={token} token={token} saveToken={saveToken} />;
}

function AccountWorkspace({ token, saveToken }: {
  token: string;
  saveToken: (token: string | null) => void;
}) {
  const [view, setView] = useState<"kanban" | "list" | "dashboard" | "agent">("kanban");
  const [projects, setProjects] = useState<Project[]>([]);
  const [project, setProject] = useState("");
  const [projectsError, setProjectsError] = useState(false);
  const [projectsLoading, setProjectsLoading] = useState(false);
  const projectsRequest = useRef(0);
  const projectsController = useRef<AbortController | null>(null);
  const accountMounted = useRef(false);
  const [sharedTask, setSharedTask] = useState<SharedTask | null>(() => readSharedTask(window.location));

  const consumeSharedTask = useCallback(() => {
    setSharedTask(null);
    const url = new URL(window.location.href);
    url.searchParams.delete("task_project");
    url.searchParams.delete("task");
    window.history.replaceState(window.history.state, "", url);
  }, []);

  const selectProject = useCallback((nextProject: string) => {
    if (sharedTask && nextProject !== sharedTask.project) {
      consumeSharedTask();
    }
    setProject(nextProject);
  }, [sharedTask, consumeSharedTask]);

  const loadProjects = useCallback(async () => {
    const request = ++projectsRequest.current;
    projectsController.current?.abort();
    const controller = new AbortController();
    projectsController.current = controller;
    const isCurrent = () => accountMounted.current && projectsRequest.current === request && localStorage.getItem("akhflow.token") === token;
    setProjectsLoading(true);
    let data: Project[];
    try {
      data = (await api("/api/projects", { signal: controller.signal }, token)) ?? [];
    } catch {
      if (isCurrent()) {
        setProjectsError(true);
        setProjectsLoading(false);
      }
      return false;
    }
    if (!isCurrent()) return false;
    setProjectsLoading(false);
    setProjectsError(false);
    setProjects(data);
    if (sharedTask && !data.some((item) => item.slug === sharedTask.project)) {
      consumeSharedTask();
    }
    const saved = localStorage.getItem("akhflow.project");
    if (data.length > 0) {
      setProject((p) => {
        if (p && data.some((x) => x.slug === p)) return p;
        if (sharedTask && data.some((x) => x.slug === sharedTask.project)) return sharedTask.project;
        if (saved && data.some((x) => x.slug === saved)) return saved;
        return data[0].slug;
      });
    } else {
      setProject("");
    }
    return true;
  }, [token, sharedTask, consumeSharedTask]);

  useEffect(() => {
    accountMounted.current = true;
    void loadProjects();
    return () => {
      accountMounted.current = false;
      projectsRequest.current++;
      projectsController.current?.abort();
    };
  }, [loadProjects]);

  return (
    <>
    {projectsError && (
      <div role="alert" className="mx-auto flex max-w-7xl flex-wrap items-center gap-2 px-4 py-2 text-sm text-destructive">
        <span>Не удалось загрузить проекты. Попробуйте ещё раз.</span>
        <Button size="sm" variant="outline" disabled={projectsLoading} onClick={() => void loadProjects()}>Повторить</Button>
      </div>
    )}
    <ProjectWorkspace
      key={`${token}:${project}`}
      token={token}
      saveToken={saveToken}
      project={project}
      projects={projects}
      setProject={selectProject}
      loadProjects={loadProjects}
      view={view}
      setView={setView}
      sharedTask={sharedTask}
      consumeSharedTask={consumeSharedTask}
    />
    </>
  );
}

type TaskCreationDraft = {
  title: string;
  revision: number;
  pending: boolean;
  error: string;
  created: number;
};

type TaskCreationStore = {
  snapshot: TaskCreationDraft;
  listeners: Set<() => void>;
};

const taskCreationDrafts = new Map<string, TaskCreationStore>();

type ChatMessageDraft = {
  text: string;
  revision: number;
  pending: boolean;
};

type ChatMessageDraftStore = {
  snapshot: Record<number, ChatMessageDraft>;
  listeners: Set<() => void>;
};

const chatMessageDrafts = new Map<string, ChatMessageDraftStore>();

function useChatMessageDrafts(token: string, project: string) {
  const context = JSON.stringify([token, project]);
  let store = chatMessageDrafts.get(context);
  if (!store) {
    store = { snapshot: {}, listeners: new Set() };
    chatMessageDrafts.set(context, store);
  }
  const draftStore = store;
  const subscribe = useCallback((listener: () => void) => {
    draftStore.listeners.add(listener);
    return () => { draftStore.listeners.delete(listener); };
  }, [draftStore]);
  const getSnapshot = useCallback(() => draftStore.snapshot, [draftStore]);
  const drafts = useSyncExternalStore(subscribe, getSnapshot);
  const update = (chatId: number, patch: Partial<ChatMessageDraft>) => {
    const current = draftStore.snapshot[chatId] ?? { text: "", revision: 0, pending: false };
    draftStore.snapshot = { ...draftStore.snapshot, [chatId]: { ...current, ...patch } };
    draftStore.listeners.forEach((listener) => listener());
  };
  const edit = (chatId: number, text: string) => {
    const revision = (draftStore.snapshot[chatId]?.revision ?? 0) + 1;
    update(chatId, { text, revision });
  };
  return { drafts, store: draftStore, update, edit };
}

type ChatCreationDraft = {
  key: string | null;
  pending: boolean;
  error: boolean;
  storageError: boolean;
  created: number | null;
  selectedAtStart: number | null;
};

type ChatCreationStore = {
  snapshot: ChatCreationDraft;
  listeners: Set<() => void>;
  controller: AbortController | null;
};

const chatCreationDrafts = new Map<string, ChatCreationStore>();

function chatCreationStorageKey(token: string, project: string): string | null {
  try {
    const payload = token.split(".")[1].replace(/-/g, "+").replace(/_/g, "/");
    const subject: unknown = JSON.parse(atob(payload)).sub;
    if (typeof subject !== "string" || !/^\d+$/.test(subject)) return null;
    return JSON.stringify(["akhflow.pendingChatCreation", subject, project]);
  } catch {
    return null;
  }
}

function createChatKey(): string {
  const bytes = crypto.getRandomValues(new Uint8Array(16));
  bytes[6] = (bytes[6] & 0x0f) | 0x40;
  bytes[8] = (bytes[8] & 0x3f) | 0x80;
  const hex = Array.from(bytes, (value) => value.toString(16).padStart(2, "0")).join("");
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}

function useChatCreationDraft(token: string, project: string) {
  const context = JSON.stringify([token, project]);
  const storageKey = chatCreationStorageKey(token, project);
  let store = chatCreationDrafts.get(context);
  if (!store) {
    let key: string | null = null;
    try {
      if (storageKey) key = sessionStorage.getItem(storageKey);
    } catch {
      key = null;
    }
    store = {
      snapshot: { key, pending: false, error: !!key, storageError: false, created: null, selectedAtStart: null },
      listeners: new Set(),
      controller: null,
    };
    chatCreationDrafts.set(context, store);
  }
  const draftStore = store;
  const subscribe = useCallback((listener: () => void) => {
    draftStore.listeners.add(listener);
    return () => { draftStore.listeners.delete(listener); };
  }, [draftStore]);
  const getSnapshot = useCallback(() => draftStore.snapshot, [draftStore]);
  const draft = useSyncExternalStore(subscribe, getSnapshot);
  const update = (patch: Partial<ChatCreationDraft>) => {
    draftStore.snapshot = { ...draftStore.snapshot, ...patch };
    draftStore.listeners.forEach((listener) => listener());
  };
  const persist = (key: string) => {
    if (!storageKey) return;
    sessionStorage.setItem(storageKey, key);
  };
  const clear = (key: string) => {
    if (!storageKey) return;
    try {
      if (sessionStorage.getItem(storageKey) === key) sessionStorage.removeItem(storageKey);
    } catch {
      // The in-memory operation remains available when storage is unavailable.
    }
  };
  return { draft, store: draftStore, update, persist, clear };
}

function useTaskCreationDraft(token: string, project: string) {
  const key = JSON.stringify([token, project]);
  let store = taskCreationDrafts.get(key);
  if (!store) {
    store = { snapshot: { title: "", revision: 0, pending: false, error: "", created: 0 }, listeners: new Set() };
    taskCreationDrafts.set(key, store);
  }
  const draftStore = store;
  const subscribe = useCallback((listener: () => void) => {
    draftStore.listeners.add(listener);
    return () => { draftStore.listeners.delete(listener); };
  }, [draftStore]);
  const getSnapshot = useCallback(() => draftStore.snapshot, [draftStore]);
  const draft = useSyncExternalStore(subscribe, getSnapshot);
  const update = (patch: Partial<TaskCreationDraft>) => {
    draftStore.snapshot = { ...draftStore.snapshot, ...patch };
    draftStore.listeners.forEach((listener) => listener());
  };
  return { draft, store: draftStore, update };
}

type WorkspaceView = "kanban" | "list" | "dashboard" | "agent";

function ProjectWorkspace({
  token, saveToken, project, projects, setProject, loadProjects, view, setView, sharedTask, consumeSharedTask,
}: {
  token: string;
  saveToken: (token: string | null) => void;
  project: string;
  projects: Project[];
  setProject: (project: string) => void;
  loadProjects: () => Promise<boolean>;
  view: WorkspaceView;
  setView: (view: WorkspaceView) => void;
  sharedTask: SharedTask | null;
  consumeSharedTask: () => void;
}) {
  const [accessError, setAccessError] = useState(false);
  const mounted = useRef(false);
  const taskRequest = useRef(0);
  const taskController = useRef<AbortController | null>(null);
  const chatsRequest = useRef(0);
  const chatsController = useRef<AbortController | null>(null);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      taskRequest.current++;
      taskController.current?.abort();
      chatsRequest.current++;
      chatsController.current?.abort();
    };
  }, []);
  const workspaceApi = useCallback<ApiRequest>((path, options, requestToken, expectedStatus) => {
    return api(path, options, requestToken, () => {
      if (mounted.current) setAccessError(true);
    }, expectedStatus);
  }, []);

  const [tasks, setTasks] = useState<Task[]>([]);
  const [tasksProject, setTasksProject] = useState("");
  const [tasksError, setTasksError] = useState(false);
  const [tasksLoading, setTasksLoading] = useState(false);
  const [chats, setChats] = useState<ChatSession[]>([]);
  const [chatsError, setChatsError] = useState(false);
  const [chatsLoading, setChatsLoading] = useState(false);
  const [chatsLoaded, setChatsLoaded] = useState(false);
  const [activeChat, setActiveChat] = useState<number | null>(null);
  const [messagesByChat, setMessagesByChat] = useState<Record<number, ChatMessage[]>>({});
  const [messageErrors, setMessageErrors] = useState<Record<number, boolean>>({});
  const [messageLoading, setMessageLoading] = useState<Record<number, boolean>>({});
  const messageRequests = useRef(new Map<number, number>());
  const messageControllers = useRef(new Map<number, AbortController>());
  const sendControllers = useRef(new Map<number, AbortController>());
  const activeChatRef = useRef(activeChat);
  activeChatRef.current = activeChat;
  const chatDrafts = useChatMessageDrafts(token, project);
  const sendGuard = useAgentMessageGuard(token, project, activeChat);
  useEffect(() => {
    return () => {
      sendControllers.current.forEach((controller) => controller.abort());
    };
  }, []);
  const creation = useTaskCreationDraft(token, project);
  const chatCreation = useChatCreationDraft(token, project);
  useEffect(() => {
    return () => { chatCreation.store.controller?.abort(); };
  }, [chatCreation.store]);
  const [createRefreshError, setCreateRefreshError] = useState("");
  const [showNewProject, setShowNewProject] = useState(false);
  const [newProjectName, setNewProjectName] = useState("");
  const [epics, setEpics] = useState<Epic[]>([]);
  const [epicsProject, setEpicsProject] = useState("");
  const [epicsError, setEpicsError] = useState(false);
  const [epicsLoading, setEpicsLoading] = useState(false);
  const epicsRequest = useRef(0);
  const epicsController = useRef<AbortController | null>(null);
  const [epicFilter, setEpicFilter] = useState<string>("all");
  const [showNewEpic, setShowNewEpic] = useState(false);
  const [newEpicTitle, setNewEpicTitle] = useState("");
  const [showTrelloImport, setShowTrelloImport] = useState(false);
  const [trelloBoard, setTrelloBoard] = useState("");
  const [trelloResult, setTrelloResult] = useState("");
  const [trelloBusy, setTrelloBusy] = useState(false);
  const [members, setMembers] = useState<Member[]>([]);
  const [membersLoaded, setMembersLoaded] = useState(false);
  const [membersError, setMembersError] = useState(false);
  const [membersLoading, setMembersLoading] = useState(false);
  const membersRequest = useRef(0);
  const membersController = useRef<AbortController | null>(null);
  const [openTaskId, setOpenTaskId] = useState<number | null>(null);
  const [showBitrixImport, setShowBitrixImport] = useState(false);
  const [bitrixGroup, setBitrixGroup] = useState("");
  const [bitrixBusy, setBitrixBusy] = useState(false);
  const [bitrixMessage, setBitrixMessage] = useState("");
  const [selectedTasks, setSelectedTasks] = useState<Set<number>>(new Set());
  const [bulkStatus, setBulkStatus] = useState("backlog");
  const [bulkBusy, setBulkBusy] = useState(false);
  const [bulkMessage, setBulkMessage] = useState("");
  const [eventNotice, setEventNotice] = useState<{ text: string; taskId: number | null } | null>(null);
  const newTaskInput = useRef<HTMLInputElement>(null);
  const projectId = projects.find((item) => item.slug === project)?.id;

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

  const loadTasks = useCallback(async () => {
    if (!mounted.current || !token || !project) return;
    const request = ++taskRequest.current;
    taskController.current?.abort();
    const controller = new AbortController();
    taskController.current = controller;
    setTasksLoading(true);
    try {
      const path = `/api/projects/${encodeURIComponent(project)}/tasks`;
      const data: Task[] = (await workspaceApi(path, { signal: controller.signal }, token)) ?? [];
      if (!mounted.current || request !== taskRequest.current) return;
      setTasks(data);
      setTasksProject(project);
      setTasksError(false);
      setCreateRefreshError("");
    } catch (error) {
      if (!mounted.current || request !== taskRequest.current || controller.signal.aborted) return;
      setTasksError(true);
      throw error;
    } finally {
      if (mounted.current && request === taskRequest.current) {
        setTasksLoading(false);
      }
    }
  }, [token, project, workspaceApi]);

  const fetchDeadlineTasks = useCallback(async (signal: AbortSignal): Promise<Task[]> => {
    return (await workspaceApi(`/api/projects/${project}/tasks`, { signal }, token)) ?? [];
  }, [token, project, workspaceApi]);

  const loadDashboard = useCallback(async (signal: AbortSignal) => {
    const [projectTasks, projectMembers, projectEpics] = await Promise.all([
      workspaceApi(`/api/projects/${project}/tasks`, { signal }, token),
      workspaceApi(`/api/projects/${project}/members`, { signal }, token),
      workspaceApi(`/api/projects/${project}/epics`, { signal }, token),
    ]);
    return { tasks: projectTasks ?? [], members: projectMembers ?? [], epics: projectEpics ?? [] };
  }, [token, project, workspaceApi]);

  const loadAgentUsage = useCallback(async (days: number, signal: AbortSignal): Promise<AgentUsageData> => {
    return workspaceApi(`/api/projects/${encodeURIComponent(project)}/agent/usage?days=${days}`, { signal }, token);
  }, [token, project, workspaceApi]);

  const loadChats = useCallback(async () => {
    if (!mounted.current || !token || !project) return;
    const request = ++chatsRequest.current;
    chatsController.current?.abort();
    const controller = new AbortController();
    chatsController.current = controller;
    const isCurrent = () => mounted.current && request === chatsRequest.current && localStorage.getItem("akhflow.token") === token;
    setChatsLoading(true);
    try {
      const path = `/api/projects/${encodeURIComponent(project)}/chats`;
      const data: ChatSession[] = (await workspaceApi(path, { signal: controller.signal }, token)) ?? [];
      if (!isCurrent()) return;
      setChats(data);
      setChatsLoaded(true);
      setChatsError(false);
      setActiveChat((prev) => {
        if (prev && data.some((c) => c.id === prev)) return prev;
        return data.length > 0 ? data[0].id : null;
      });
    } catch {
      if (isCurrent() && !controller.signal.aborted) setChatsError(true);
    } finally {
      if (isCurrent()) setChatsLoading(false);
    }
  }, [token, project, workspaceApi]);

  useEffect(() => {
    const result = chatCreation.draft.created;
    if (result === null || !mounted.current || localStorage.getItem("akhflow.token") !== token) return;
    if (chatCreation.store.snapshot.created !== result) return;
    const selectedAtStart = chatCreation.store.snapshot.selectedAtStart;
    chatCreation.update({ created: null });
    setChats((current) => {
      if (current.some((chat) => chat.id === result)) return current;
      return [{ id: result, title: "", created_at: "" }, ...current];
    });
    if (activeChatRef.current === selectedAtStart) setActiveChat(result);
    void loadChats();
  }, [chatCreation.draft.created, chatCreation.store, loadChats, token]);

  const loadMessages = useCallback(
    async (chatId: number) => {
      if (!mounted.current || chatDrafts.store.snapshot[chatId]?.pending) {
        return false;
      }
      const request = (messageRequests.current.get(chatId) ?? 0) + 1;
      messageRequests.current.set(chatId, request);
      messageControllers.current.get(chatId)?.abort();
      const controller = new AbortController();
      messageControllers.current.set(chatId, controller);
      const isCurrent = () => mounted.current && !controller.signal.aborted &&
        messageRequests.current.get(chatId) === request && localStorage.getItem("akhflow.token") === token;
      setMessageLoading((current) => ({ ...current, [chatId]: true }));
      try {
        const path = `/api/chats/${chatId}/messages`;
        const data: unknown = await workspaceApi(path, { signal: controller.signal }, token, 200);
        if (!isChatHistory(data)) throw new Error("invalid chat history");
        if (!isCurrent()) return false;
        setMessagesByChat((current) => ({ ...current, [chatId]: data }));
        setMessageErrors((current) => ({ ...current, [chatId]: false }));
        return true;
      } catch {
        if (!isCurrent()) return false;
        setMessageErrors((current) => ({ ...current, [chatId]: true }));
        return false;
      } finally {
        if (isCurrent()) {
          setMessageLoading((current) => ({ ...current, [chatId]: false }));
        }
      }
    },
    [token, workspaceApi, chatDrafts.store],
  );

  useEffect(() => {
    loadChats();
  }, [loadChats]);

  useEffect(() => {
    if (!activeChat) return;
    void loadMessages(activeChat);
    return () => {
      messageControllers.current.get(activeChat)?.abort();
      messageRequests.current.set(activeChat, (messageRequests.current.get(activeChat) ?? 0) + 1);
    };
  }, [activeChat, loadMessages]);

  const activeChatPending = activeChat ? chatDrafts.drafts[activeChat]?.pending ?? false : false;
  const waitingForChat = useRef<number | null>(null);
  useEffect(() => {
    if (!activeChat) {
      waitingForChat.current = null;
      return;
    }
    if (activeChatPending) {
      waitingForChat.current = activeChat;
      return;
    }
    if (waitingForChat.current === activeChat && !messagesByChat[activeChat]) {
      void loadMessages(activeChat);
    }
    waitingForChat.current = null;
  }, [activeChat, activeChatPending, loadMessages, messagesByChat]);

  const loadEpics = useCallback(async () => {
    if (!token || !project || !mounted.current) return;
    const request = ++epicsRequest.current;
    epicsController.current?.abort();
    const controller = new AbortController();
    epicsController.current = controller;
    const isCurrent = () => mounted.current && request === epicsRequest.current && localStorage.getItem("akhflow.token") === token;
    setEpicsLoading(true);
    try {
      const path = `/api/projects/${encodeURIComponent(project)}/epics`;
      const data: Epic[] = (await workspaceApi(path, { signal: controller.signal }, token)) ?? [];
      if (!isCurrent()) return;
      setEpics(data);
      setEpicsProject(project);
      setEpicsError(false);
    } catch (error) {
      if (!isCurrent()) return;
      setEpicsError(true);
      throw error;
    } finally {
      if (isCurrent()) setEpicsLoading(false);
    }
  }, [token, project, workspaceApi]);

  useEffect(() => {
    void loadEpics().catch(() => {});
    return () => {
      epicsRequest.current++;
      epicsController.current?.abort();
    };
  }, [loadEpics]);

  const loadMembers = useCallback(async () => {
    if (!token || !project || !mounted.current) return;
    const request = ++membersRequest.current;
    membersController.current?.abort();
    const controller = new AbortController();
    membersController.current = controller;
    const isCurrent = () => mounted.current && request === membersRequest.current && localStorage.getItem("akhflow.token") === token;
    setMembersLoading(true);
    try {
      const path = `/api/projects/${encodeURIComponent(project)}/members`;
      const data: Member[] = (await workspaceApi(path, { signal: controller.signal }, token)) ?? [];
      if (!isCurrent()) return;
      setMembers(data);
      setMembersLoaded(true);
      setMembersError(false);
    } catch (error) {
      if (!isCurrent() || controller.signal.aborted) return;
      setMembersError(true);
      throw error;
    } finally {
      if (isCurrent()) setMembersLoading(false);
    }
  }, [token, project, workspaceApi]);

  useEffect(() => {
    void loadMembers().catch(() => {});
    return () => {
      membersRequest.current++;
      membersController.current?.abort();
    };
  }, [loadMembers]);

  const visibleTasks = tasks.filter((t) =>
    epicFilter === "all" ? true : epicFilter === "none" ? !t.epic_id : t.epic_id === Number(epicFilter),
  );
  const selectedVisibleTasks = visibleTasks.filter((task) => selectedTasks.has(task.id));

  useEffect(() => {
    let active = true;
    void loadTasks().catch(() => {
      if (active && creation.draft.created > 0) {
        setCreateRefreshError("Задача создана, но список не обновился. Обнови список ещё раз.");
      }
    });
    return () => { active = false; };
  }, [loadTasks, project, creation.draft.created]);

  useEffect(() => {
    if (!project || !projectId) return;
    const controller = new AbortController();
    let refreshing = false;
    let refreshPending = false;
    let noticeTimer: number | undefined;
    const refresh = async () => {
      refreshPending = true;
      if (refreshing) return;
      refreshing = true;
      while (refreshPending && !controller.signal.aborted) {
        refreshPending = false;
        try {
          await loadTasks();
        } catch {
          // The task-list error offers a retry until the next event or reconnection.
        }
      }
      refreshing = false;
    };
    const labels: Record<string, string> = {
      task_created: "Создана задача",
      tasks_imported: "Данные проекта импортированы",
      task_moved: "Изменён статус задачи",
      task_updated: "Изменена задача",
      task_deleted: "Удалена задача",
      task_comment: "Новый комментарий к задаче",
      dependency_added: "Добавлена зависимость задачи",
      dependency_removed: "Удалена зависимость задачи",
    };
    const path = `${API}/api/projects/${encodeURIComponent(project)}/events`;
    const unsubscribe = subscribeProjectEvents(path, token, {
      onConnect: () => { void refresh(); },
      onEvent: (value) => {
        if (controller.signal.aborted || !value || typeof value !== "object") return;
        const event = value as Record<string, unknown>;
        if (event.project_id !== projectId || typeof event.type !== "string" || !Object.hasOwn(labels, event.type)) return;
        if (event.type === "tasks_imported") void loadEpics().catch(() => {});
        const title = typeof event.title === "string" ? event.title : "";
        const text = title ? `${labels[event.type]}: ${title}` : labels[event.type];
        const validTaskId = typeof event.task_id === "number" && Number.isSafeInteger(event.task_id) && event.task_id > 0;
        const taskId = validTaskId && event.type !== "task_deleted" ? event.task_id as number : null;
        setEventNotice({ text, taskId });
        window.clearTimeout(noticeTimer);
        noticeTimer = window.setTimeout(() => setEventNotice(null), 6000);
        void refresh();
      },
    });
    return () => {
      controller.abort();
      unsubscribe();
      window.clearTimeout(noticeTimer);
    };
  }, [loadTasks, loadEpics, project, projectId, token]);

  useEffect(() => {
    if (!sharedTask || sharedTask.project !== project || tasksProject !== project) return;
    if (tasks.some((task) => task.id === sharedTask.taskId)) {
      setOpenTaskId(sharedTask.taskId);
    }
    consumeSharedTask();
  }, [sharedTask, project, tasks, tasksProject, consumeSharedTask]);

  const selectTask = (id: number, checked: boolean) => {
    setSelectedTasks((previous) => {
      const next = new Set(previous);
      if (checked) {
        next.add(id);
      } else {
        next.delete(id);
      }
      return next;
    });
    setBulkMessage("");
  };

  const moveSelectedTasks = async () => {
    if (bulkBusy || selectedVisibleTasks.length === 0) {
      return;
    }
    setBulkBusy(true);
    setBulkMessage("");
    const moved: number[] = [];
    const failed: number[] = [];
    let unchanged = 0;
    for (const task of selectedVisibleTasks) {
      if (task.status === bulkStatus) {
        unchanged++;
        continue;
      }
      try {
        await workspaceApi(`/api/tasks/${task.id}`, {
          method: "PATCH",
          body: JSON.stringify({ status: bulkStatus }),
        }, token);
        moved.push(task.id);
      } catch {
        failed.push(task.id);
      }
    }
    setTasks((previous) => previous.map((task) => {
      if (moved.includes(task.id)) {
        return { ...task, status: bulkStatus };
      }
      return task;
    }));
    setSelectedTasks(new Set(failed));
    let message = `Перемещено: ${moved.length}.`;
    if (unchanged > 0) {
      message += ` Уже в выбранном статусе: ${unchanged}.`;
    }
    if (failed.length > 0) {
      const ids = failed.map((id) => `#${id}`).join(", ");
      message += ` Не удалось переместить: ${ids}. Проверь состояние задач и повтори.`;
    }
    try {
      await loadTasks();
    } catch {
      message += " Не удалось обновить список задач.";
    }
    setBulkMessage(message);
    setBulkBusy(false);
  };

  const move = async (id: number, dir: number) => {
    const t = tasks.find((x) => x.id === id);
    if (!t) return;
    const i = COLUMNS.findIndex((c) => c.id === t.status);
    const target = COLUMNS[i + dir];
    if (!target) return;
    await workspaceApi(`/api/tasks/${id}`, { method: "PATCH", body: JSON.stringify({ status: target.id }) }, token);
    await loadTasks();
  };

  const createProject = async () => {
    if (!newProjectName.trim()) return;
    const data: Project = await workspaceApi("/api/projects", {
      method: "POST",
      body: JSON.stringify({ name: newProjectName.trim() }),
    }, token);
    setShowNewProject(false);
    setNewProjectName("");
    if (!await loadProjects()) return;
    if (!mounted.current) return;
    setProject(data.slug);
    localStorage.setItem("akhflow.project", data.slug);
  };

  const createEpic = async () => {
    if (!newEpicTitle.trim()) return;
    await workspaceApi(`/api/projects/${project}/epics`, {
      method: "POST",
      body: JSON.stringify({ title: newEpicTitle.trim() }),
    }, token);
    setShowNewEpic(false);
    setNewEpicTitle("");
    void loadEpics().catch(() => {});
  };

  const importTrello = async () => {
    if (!trelloBoard.trim() || trelloBusy) return;
    setTrelloBusy(true);
    setTrelloResult("");
    try {
      const result = await workspaceApi(`/api/projects/${project}/imports/trello`, {
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
    await workspaceApi(`/api/tasks/${taskId}`, {
      method: "PATCH",
      body: JSON.stringify({ epic_id: epicId }),
    }, token);
    loadTasks();
  };

  const createTask = async () => {
    const submitted = creation.store.snapshot;
    if (!project || submitted.pending || !submitted.title.trim()) return;
    creation.update({ pending: true, error: "" });
    setCreateRefreshError("");
    try {
      await workspaceApi(`/api/projects/${project}/tasks`, {
        method: "POST",
        body: JSON.stringify({ title: submitted.title, status: "backlog" }),
      }, token);
      const current = creation.store.snapshot;
      const title = current.revision === submitted.revision ? "" : current.title;
      creation.update({ title, created: current.created + 1 });
    } catch {
      creation.update({ error: `Не удалось подтвердить создание задачи «${submitted.title}». Проверь список перед повторной отправкой. Введённый текст сохранён.` });
    } finally {
      creation.update({ pending: false });
    }
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
      const result = await workspaceApi(`/api/projects/${project}/imports/bitrix24`, {
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
    if (!project || chatCreation.store.snapshot.pending) return;
    let key = chatCreation.store.snapshot.key;
    try {
      if (!key) {
        key = createChatKey();
        chatCreation.persist(key);
      }
    } catch {
      chatCreation.update({ error: true, storageError: true });
      return;
    }
    const controller = new AbortController();
    chatCreation.store.controller = controller;
    chatCreation.update({ key, pending: true, error: false, storageError: false, selectedAtStart: activeChatRef.current });
    try {
      const path = `/api/projects/${encodeURIComponent(project)}/chats`;
      const options = { method: "POST", body: "{}", headers: { "Idempotency-Key": key }, signal: controller.signal };
      const data = await workspaceApi(path, options, token);
      if (!Number.isSafeInteger(data.id) || data.id <= 0) throw new Error("invalid chat id");
      chatCreation.clear(key);
      chatCreation.update({ key: null, pending: false, error: false, created: data.id });
    } catch {
      chatCreation.update({ pending: false, error: true });
    } finally {
      if (chatCreation.store.controller === controller) chatCreation.store.controller = null;
    }
  };

  const askAgent = async () => {
    if (!activeChat || !messagesByChat[activeChat]) return;
    const chatId = activeChat;
    const draft = chatDrafts.store.snapshot[chatId];
    if (!draft || !draft.text.trim() || draft.pending) return;
    if (!sendGuard.state?.ready || sendGuard.state.pending || sendGuard.state.storageError) return;
    const text = draft.text.trim();
    const operation = sendGuard.remember(text);
    if (!operation) return;
    chatDrafts.update(chatId, { text: "", revision: draft.revision + 1, pending: true });
    messageControllers.current.get(chatId)?.abort();
    messageRequests.current.set(chatId, (messageRequests.current.get(chatId) ?? 0) + 1);
    setMessageLoading((current) => ({ ...current, [chatId]: false }));
    const tempId = Date.now();
    setMessagesByChat((current) => ({
      ...current,
      [chatId]: [...(current[chatId] ?? []), { id: tempId, role: "user", content: text }],
    }));
    let sent = false;
    const controller = new AbortController();
    sendControllers.current.set(chatId, controller);
    try {
      const data = await workspaceApi(`/api/chats/${chatId}/messages`, {
        method: "POST",
        body: JSON.stringify({ message: text }),
        signal: controller.signal,
      }, token, 200);
      if (!isCompletedAgentResponse(data)) throw new Error("invalid agent response");
      sendGuard.clear(operation.key, operation.id);
      if (!mounted.current || controller.signal.aborted || localStorage.getItem("akhflow.token") !== token) return;
      setMessagesByChat((current) => ({
        ...current,
        [chatId]: [...(current[chatId] ?? []), { id: tempId + 1, role: "agent", content: data.reply, tool_calls: data.tool_calls ?? undefined }],
      }));
      sent = true;
    } catch {
      // A failed response does not prove that the agent did not act.
    } finally {
      if (sendControllers.current.get(chatId) === controller) sendControllers.current.delete(chatId);
      chatDrafts.update(chatId, { pending: false });
    }
    if (sent && mounted.current && activeChatRef.current === chatId) {
      void loadMessages(chatId);
    }
    void loadTasks().catch(() => {});
    void loadChats();
  };

  const checkUncertainMessage = async () => {
    const pending = sendGuard.state?.pending;
    if (!activeChat || !pending || chatDrafts.store.snapshot[activeChat]?.pending) return;
    sendGuard.setChecked(pending.id, false);
    if (await loadMessages(activeChat)) sendGuard.setChecked(pending.id, true);
  };

  const messages = activeChat ? messagesByChat[activeChat] ?? [] : [];
  const busy = activeChat ? chatDrafts.drafts[activeChat]?.pending ?? false : false;
  const input = activeChat ? chatDrafts.drafts[activeChat]?.text ?? "" : "";

  return (
    <div className="min-h-screen bg-background">
      <header className="border-b">
        <div className="max-w-7xl mx-auto px-4 min-h-14 flex flex-wrap items-center gap-4 py-2">
          <span className="font-semibold">Akhflow-AI</span>
          <select
            className="h-8 rounded-md border bg-transparent px-2 text-sm"
            value={project}
            disabled={bulkBusy}
            onChange={(e) => {
              setSelectedTasks(new Set());
              setBulkMessage("");
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
          {project && <AkhcheckImport project={project} token={token} onImported={loadTasks} request={workspaceApi} />}
          <select
            className="h-8 rounded-md border bg-transparent px-2 text-sm"
            value={epicFilter}
            disabled={bulkBusy}
            onChange={(e) => {
              setSelectedTasks(new Set());
              setBulkMessage("");
              setEpicFilter(e.target.value);
            }}
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
            <ThemeToggle />
            <EmailNotifications token={token} request={workspaceApi} />
            <Button variant="ghost" size="sm" onClick={() => { saveToken(null); }}>Выйти</Button>
          </div>
        </div>
      </header>

      <main className="max-w-7xl mx-auto px-4 py-6 space-y-4">
        {membersError && !openTaskId && (
          <div role="alert" className="flex flex-wrap items-center gap-2 rounded-md border border-destructive p-3 text-sm">
            <span>Не удалось загрузить участников проекта. Попробуй ещё раз.</span>
            <Button variant="outline" size="sm" disabled={membersLoading} onClick={() => void loadMembers().catch(() => {})}>
              Повторить загрузку участников
            </Button>
          </div>
        )}
        {epicsError && (
          <div role="alert" className="flex flex-wrap items-center gap-2 rounded-md border border-destructive p-3 text-sm">
            <span>Не удалось загрузить эпики. Попробуй ещё раз.</span>
            <Button variant="outline" size="sm" disabled={epicsLoading} onClick={() => void loadEpics().catch(() => {})}>
              Повторить загрузку эпиков
            </Button>
          </div>
        )}
        {accessError && (
          <div role="alert" className="rounded-md border border-destructive p-3 text-sm">
            Недостаточно прав для выполнения действия. Обратись к владельцу проекта.
            <Button variant="ghost" size="sm" onClick={() => setAccessError(false)}>Закрыть</Button>
          </div>
        )}
        {project && <ProjectMembers project={project} token={token} request={workspaceApi} onMembersChanged={loadMembers} />}
        {project && (
          <QuickSearch
            tasks={tasksProject === project ? tasks : []}
            epics={epicsProject === project ? epics : []}
            ready={tasksProject === project && epicsProject === project}
            onTaskOpen={setOpenTaskId}
            onEpicOpen={(id) => {
              setEpicFilter(String(id));
              setView("kanban");
            }}
          />
        )}
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
            userId={reminderUserId(token)}
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
            disabled={!project}
            value={creation.draft.title}
            onChange={(e) => creation.update({ title: e.target.value, revision: creation.store.snapshot.revision + 1 })}
            onKeyDown={(e) => {
              if (e.key === "Enter" && !e.nativeEvent.isComposing) {
                e.preventDefault();
                void createTask();
              }
            }}
          />
          <Button size="sm" disabled={creation.draft.pending || !project || !creation.draft.title.trim()} onClick={() => void createTask()}><Plus /> Добавить</Button>
        </div>
        {creation.draft.pending && <p role="status" className="text-sm text-muted-foreground">Создаём задачу...</p>}
        {creation.draft.error && (
          <div role="alert" className="text-sm text-destructive">
            {creation.draft.error}
            <Button size="sm" variant="ghost" onClick={() => {
              void loadTasks().catch(() => setCreateRefreshError("Не удалось обновить список. Попробуй ещё раз."));
            }}>Проверить список задач</Button>
          </div>
        )}
        {createRefreshError && (
          <div role="alert" className="text-sm text-destructive">
            {createRefreshError}
            <Button size="sm" variant="ghost" onClick={() => {
              void loadTasks().then(() => setCreateRefreshError("")).catch(() => {});
            }}>Обновить список</Button>
          </div>
        )}
        {tasksError && (
          <div role="alert" className="flex flex-wrap items-center gap-2 text-sm text-destructive">
            <span>Не удалось загрузить задачи. Попробуй ещё раз.</span>
            <Button size="sm" variant="outline" disabled={tasksLoading} onClick={() => void loadTasks().catch(() => {})}>
              Повторить загрузку задач
            </Button>
          </div>
        )}
        <p className="text-sm text-muted-foreground">Alt+N: перейти к созданию задачи.</p>

        {(view === "kanban" || view === "list") && (
          <section aria-label="Массовые операции" className="flex flex-wrap items-center gap-2">
            <Button
              variant="outline"
              size="sm"
              disabled={bulkBusy || visibleTasks.length === 0}
              onClick={() => setSelectedTasks(new Set(visibleTasks.map((task) => task.id)))}
            >
              Выбрать видимые
            </Button>
            <Button
              variant="outline"
              size="sm"
              disabled={bulkBusy || selectedVisibleTasks.length === 0}
              onClick={() => setSelectedTasks(new Set())}
            >
              Снять выбор
            </Button>
            <span>Выбрано: {selectedVisibleTasks.length}</span>
            <label htmlFor="bulk-status">Статус</label>
            <select
              id="bulk-status"
              className="h-8 rounded-md border bg-transparent px-2 text-sm"
              value={bulkStatus}
              disabled={bulkBusy}
              onChange={(event) => setBulkStatus(event.target.value)}
            >
              {COLUMNS.map((column) => <option key={column.id} value={column.id}>{column.label}</option>)}
            </select>
            <Button size="sm" disabled={bulkBusy || selectedVisibleTasks.length === 0} onClick={moveSelectedTasks}>
              {bulkBusy ? "Перемещаем…" : "Переместить выбранные"}
            </Button>
            {bulkMessage && <p role="status" className="w-full text-sm">{bulkMessage}</p>}
          </section>
        )}

        {view === "kanban" && (
          <Kanban tasks={visibleTasks} allTasks={tasks} epics={epics} selected={selectedTasks} selectionDisabled={bulkBusy} onSelect={selectTask} onOpen={setOpenTaskId} onMove={move} />
        )}

        {view === "dashboard" && (
          project
            ? <ProjectDashboard load={loadDashboard} revision={tasks} epicsRevision={epics} project={project} token={token} request={workspaceApi} />
            : <p className="text-sm text-muted-foreground">Создай проект, чтобы увидеть сводку.</p>
        )}

        {view === "list" && (
          <Card>
            <CardContent className="p-0">
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b text-left text-muted-foreground">
                    <th className="p-3 font-medium">Выбор</th>
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
                      <td className="p-3">
                        <input
                          type="checkbox"
                          aria-label={`Выбрать задачу ${t.title} #${t.id}`}
                          checked={selectedTasks.has(t.id)}
                          disabled={bulkBusy}
                          onChange={(event) => selectTask(t.id, event.target.checked)}
                        />
                      </td>
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
          <div className="space-y-4">
          {project && <AgentUsage load={loadAgentUsage} revision={chats} />}
          <div className="grid grid-cols-[240px_1fr] gap-4">
            <Card>
              <CardHeader className="flex flex-row items-center justify-between pb-2">
                <CardTitle className="text-base">Чаты</CardTitle>
                <Button size="sm" variant="outline" aria-label="Создать чат" disabled={!project || chatCreation.draft.pending || !!chatCreation.draft.key} onClick={newChat}><Plus /></Button>
              </CardHeader>
              <CardContent className="space-y-1 p-3">
                {chatCreation.draft.error && (
                  <div role="alert" className="space-y-2 text-sm text-destructive">
                    {chatCreation.draft.storageError
                      ? <p>Не удалось подготовить создание чата. Проверь доступ к хранилищу браузера.</p>
                      : <p>Не удалось подтвердить создание чата. Повтори попытку: дубликат не появится.</p>}
                    <Button size="sm" variant="outline" disabled={chatCreation.draft.pending} onClick={newChat}>Повторить создание</Button>
                  </div>
                )}
                {chatsError && (
                  <div role="alert" className="space-y-2 text-sm text-destructive">
                    <p>Не удалось загрузить чаты.</p>
                    <Button size="sm" variant="outline" disabled={chatsLoading} onClick={() => void loadChats()}>Повторить</Button>
                  </div>
                )}
                {chatsLoaded && !chatsError && chats.length === 0 && <p className="text-sm text-muted-foreground">Нет чатов</p>}
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
                {activeChat && messageErrors[activeChat] && (
                  <div role="alert" className="space-y-2 text-sm text-destructive">
                    <p>Не удалось загрузить историю чата.</p>
                    <Button size="sm" variant="outline" disabled={messageLoading[activeChat]} onClick={() => void loadMessages(activeChat)}>
                      Повторить загрузку истории
                    </Button>
                  </div>
                )}
                {activeChat && !messageErrors[activeChat] && messagesByChat[activeChat] && messages.length === 0 && (
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
                  <div className="space-y-2">
                    {sendGuard.state?.storageError && (
                      <p role="alert" className="text-sm text-destructive">Не удалось сохранить состояние отправки. Проверь доступ к хранилищу браузера и обнови страницу.</p>
                    )}
                    {sendGuard.state?.acknowledged && (
                      <p className="text-sm text-destructive">Предыдущее действие с неизвестным результатом всё ещё может выполняться. Учитывай это перед новой отправкой.</p>
                    )}
                    {sendGuard.state?.pending && !sendGuard.state.storageError && !busy && (
                      <div role="alert" className="space-y-2 text-sm text-destructive">
                        <p>Результат отправки неизвестен. Агент мог выполнить действие или ещё выполнять его. Проверь историю перед продолжением.</p>
                        <p className="whitespace-pre-wrap">{sendGuard.state.pending.text}</p>
                        <Button size="sm" variant="outline" disabled={messageLoading[activeChat]} onClick={() => void checkUncertainMessage()}>Проверить историю</Button>
                        {sendGuard.state.checked && (
                          <Button size="sm" variant="outline" onClick={() => {
                            const pending = sendGuard.state?.pending;
                            const key = sendGuard.state?.storageKey;
                            if (pending && key) sendGuard.acknowledge(key, pending.id);
                          }}>Я проверил историю и понимаю, что действие может продолжаться</Button>
                        )}
                      </div>
                    )}
                    <div className="flex gap-2">
                    <Input
                      value={input}
                      onChange={(e) => chatDrafts.edit(activeChat, e.target.value)}
                      onKeyDown={(e) => e.key === "Enter" && askAgent()}
                      placeholder="Скажи агенту, что сделать..."
                    />
                    <Button onClick={askAgent} disabled={busy || !messagesByChat[activeChat] || !sendGuard.state?.ready || !!sendGuard.state.pending || sendGuard.state.storageError}><Send /></Button>
                    </div>
                  </div>
                )}
              </CardContent>
            </Card>
          </div>
          </div>
        )}
      </main>

      {eventNotice && (
        <div className="fixed bottom-4 right-4 z-50 rounded-md border bg-background p-3 shadow-lg" role="status">
          {eventNotice.taskId ? (
            <button type="button" onClick={() => { setOpenTaskId(eventNotice.taskId); setEventNotice(null); }}>
              {eventNotice.text}
            </button>
          ) : eventNotice.text}
        </div>
      )}

      {openTaskId && (
        <TaskDetail
          key={openTaskId}
          taskId={openTaskId}
          project={project}
          epics={epics}
          members={members}
          membersLoaded={membersLoaded}
          membersError={membersError}
          membersLoading={membersLoading}
          onRetryMembers={() => void loadMembers().catch(() => {})}
          tasks={tasks}
          recommendationReady={membersLoaded && tasksProject === project}
          onOpen={setOpenTaskId}
          onClose={() => setOpenTaskId(null)}
          onChanged={loadTasks}
          request={workspaceApi}
        />
      )}
    </div>
  );
}
