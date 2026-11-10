import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { summarizeDashboard } from "@/lib/dashboard";
import type { DashboardMember, DashboardRow, DashboardTask } from "@/lib/dashboard";

type DashboardData = {
  tasks: DashboardTask[];
  members: DashboardMember[];
};

type Props = {
  load: (signal: AbortSignal) => Promise<DashboardData>;
  revision: unknown;
};

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

export function ProjectDashboard({ load, revision }: Props) {
  const [data, setData] = useState<DashboardData | null>(null);
  const [error, setError] = useState("");
  const [refresh, setRefresh] = useState(0);
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
  }, [load, refresh, revision]);

  const summary = data && summarizeDashboard(data.tasks, data.members);
  return (
    <section aria-label="Сводка проекта" className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="text-xl font-semibold">Сводка проекта</h2>
          <p className="mt-1 text-sm text-muted-foreground">Все задачи проекта, независимо от фильтра эпика.</p>
        </div>
        <Button variant="outline" size="sm" onClick={() => setRefresh((value) => value + 1)}>Обновить</Button>
      </div>
      {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
      {!summary && !error && <p role="status" className="text-sm text-muted-foreground">Загружаем сводку проекта.</p>}
      {summary && (
        <>
          <p className="text-sm">Всего задач: <span className="font-semibold tabular-nums">{summary.total}</span></p>
          {summary.total === 0 && <p className="text-sm text-muted-foreground">В проекте пока нет задач.</p>}
          <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
            <Breakdown title="По статусам" rows={summary.statuses} total={summary.total} />
            <Breakdown title="По приоритетам" rows={summary.priorities} total={summary.total} />
            <Breakdown title="По исполнителям" rows={summary.assignees} total={summary.total} />
          </div>
        </>
      )}
    </section>
  );
}
