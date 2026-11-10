import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { summarizeDashboard, summarizeEpics } from "@/lib/dashboard";
import type { DashboardEpic, DashboardMember, DashboardRow, DashboardTask } from "@/lib/dashboard";

type DashboardData = {
  tasks: DashboardTask[];
  members: DashboardMember[];
  epics: DashboardEpic[];
};

type Props = {
  load: (signal: AbortSignal) => Promise<DashboardData>;
  revision: unknown;
  epicsRevision: unknown;
  project: string;
  token: string;
  request: (path: string, options?: RequestInit, token?: string | null) => Promise<any>;
};

const WIDGETS = [
  { id: "statuses", label: "По статусам" },
  { id: "priorities", label: "По приоритетам" },
  { id: "assignees", label: "По исполнителям" },
  { id: "epics", label: "Прогресс эпиков" },
] as const;

function Breakdown({ title, rows, total }: { title: string; rows: DashboardRow[]; total: number }) {
  return (
    <Card>
      <CardHeader><CardTitle className="text-base">{title}</CardTitle></CardHeader>
      <CardContent>
        <ul className="space-y-3">
          {rows.map((row) => (
            <li key={row.id}>
              <div className="flex justify-between gap-3 text-sm">
                <span className="min-w-0 break-words">{row.label}</span>
                <span className="tabular-nums font-medium">{row.count}</span>
              </div>
              <div className="mt-1 h-2 overflow-hidden rounded-full bg-muted" aria-hidden="true">
                <div
                  className="h-full rounded-full bg-primary"
                  style={{ width: `${total ? (row.count / total) * 100 : 0}%` }}
                />
              </div>
            </li>
          ))}
        </ul>
      </CardContent>
    </Card>
  );
}

export function ProjectDashboard({ load, revision, epicsRevision, project, token, request }: Props) {
  const [data, setData] = useState<DashboardData | null>(null);
  const [error, setError] = useState("");
  const [refresh, setRefresh] = useState(0);
  const [hidden, setHidden] = useState<string[] | null>(null);
  const [settingsError, setSettingsError] = useState("");
  const [saving, setSaving] = useState(false);
  const [settingsRefresh, setSettingsRefresh] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    setHidden(null);
    setSettingsError("");
    request(`/api/projects/${project}/dashboard-preferences`, { signal: controller.signal }, token)
      .then((result) => {
        if (!controller.signal.aborted) setHidden(result.hidden_widgets);
      })
      .catch(() => {
        if (!controller.signal.aborted) setSettingsError("Не удалось загрузить настройки виджетов.");
      });
    return () => controller.abort();
  }, [project, token, request, settingsRefresh]);

  const toggleWidget = async (id: string) => {
    if (!hidden || saving) return;
    const next = hidden.includes(id) ? hidden.filter((item) => item !== id) : [...hidden, id];
    setHidden(next);
    setSaving(true);
    setSettingsError("");
    try {
      await request(`/api/projects/${project}/dashboard-preferences`, { method: "PUT", body: JSON.stringify({ hidden_widgets: next }) }, token);
    } catch {
      setHidden(hidden);
      setSettingsError("Не удалось сохранить настройки виджетов.");
    } finally {
      setSaving(false);
    }
  };
  useEffect(() => {
    const controller = new AbortController();
    setError("");
    load(controller.signal).then((next) => {
      if (!controller.signal.aborted) {
        setData(next);
      }
    }).catch(() => {
      if (!controller.signal.aborted) {
        setData(null);
        setError("Не удалось загрузить сводку проекта.");
      }
    });
    return () => controller.abort();
  }, [load, refresh, revision, epicsRevision]);

  const summary = data && summarizeDashboard(data.tasks, data.members);
  const epicProgress = data && summarizeEpics(data.tasks, data.epics);
  return (
    <section aria-label="Сводка проекта" className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="text-xl font-semibold">Сводка проекта</h2>
          <p className="mt-1 text-sm text-muted-foreground">Все задачи проекта, независимо от фильтра эпика.</p>
        </div>
        <Button variant="outline" size="sm" onClick={() => setRefresh((value) => value + 1)}>Обновить</Button>
      </div>
      <fieldset className="rounded-md border p-3" disabled={hidden === null || saving}>
        <legend className="px-1 text-sm font-medium">Виджеты сводки</legend>
        <div className="flex flex-wrap gap-x-4 gap-y-2">
          {WIDGETS.map((widget) => (
            <label key={widget.id} className="flex items-center gap-2 text-sm">
              <input type="checkbox" checked={hidden !== null && !hidden.includes(widget.id)} onChange={() => void toggleWidget(widget.id)} />
              {widget.label}
            </label>
          ))}
        </div>
      </fieldset>
      {settingsError && <div role="alert" className="text-sm text-destructive">{settingsError}{hidden === null && <Button variant="ghost" size="sm" onClick={() => setSettingsRefresh((value) => value + 1)}>Повторить</Button>}</div>}
      {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
      {!summary && !error && <p role="status" className="text-sm text-muted-foreground">Загружаем сводку проекта.</p>}
      {summary && (
        <>
          <p className="text-sm">Всего задач: <span className="font-semibold tabular-nums">{summary.total}</span></p>
          {summary.total === 0 && <p className="text-sm text-muted-foreground">В проекте пока нет задач.</p>}
          <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
            {hidden !== null && !hidden.includes("statuses") && <Breakdown title="По статусам" rows={summary.statuses} total={summary.total} />}
            {hidden !== null && !hidden.includes("priorities") && <Breakdown title="По приоритетам" rows={summary.priorities} total={summary.total} />}
            {hidden !== null && !hidden.includes("assignees") && <Breakdown title="По исполнителям" rows={summary.assignees} total={summary.total} />}
          </div>
          {hidden !== null && !hidden.includes("epics") && <Card>
            <CardHeader><CardTitle className="text-base">Прогресс эпиков</CardTitle></CardHeader>
            <CardContent>
              {epicProgress?.length === 0 && (
                <p className="text-sm text-muted-foreground">В проекте пока нет эпиков.</p>
              )}
              <ul className="space-y-4">
                {epicProgress?.map((epic) => (
                  <li key={epic.id}>
                    <div className="flex flex-wrap justify-between gap-2 text-sm">
                      <span className="min-w-0 break-words font-medium">{epic.title}</span>
                      <span className="tabular-nums">{epic.done} из {epic.total} · {epic.percent}%</span>
                    </div>
                    <progress
                      className="mt-1 h-2 w-full accent-primary"
                      max={100}
                      value={epic.percent}
                      aria-label={`Прогресс эпика ${epic.title}`}
                      aria-valuetext={`${epic.done} из ${epic.total}, ${epic.percent}%`}
                    />
                  </li>
                ))}
              </ul>
            </CardContent>
          </Card>}
        </>
      )}
    </section>
  );
}
