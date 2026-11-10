import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";

export type AgentUsageData = {
  days: number;
  requests: number;
  replies: number;
  tool_calls: number;
};

type Props = {
  load: (days: number, signal: AbortSignal) => Promise<AgentUsageData>;
  revision: unknown;
};

export function AgentUsage({ load, revision }: Props) {
  const [days, setDays] = useState(30);
  const [refresh, setRefresh] = useState(0);
  const [usage, setUsage] = useState<AgentUsageData | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    const controller = new AbortController();
    setUsage(null);
    setError("");
    load(days, controller.signal).then((result) => {
      if (!controller.signal.aborted) {
        setUsage(result);
      }
    }).catch(() => {
      if (!controller.signal.aborted) {
        setError("Не удалось загрузить метрики агента.");
      }
    });
    return () => controller.abort();
  }, [days, load, refresh, revision]);

  return (
    <Card>
      <CardHeader className="flex flex-wrap flex-row items-center justify-between gap-2">
        <CardTitle className="text-base">Использование агента</CardTitle>
        <div className="flex items-center gap-2">
          <label htmlFor="agent-usage-days" className="text-sm">Период</label>
          <select
            id="agent-usage-days"
            className="rounded border bg-background px-2 py-1 text-sm"
            value={days}
            onChange={(event) => setDays(Number(event.target.value))}
          >
            <option value={7}>7 дней</option>
            <option value={30}>30 дней</option>
            <option value={0}>Всё время</option>
          </select>
          <Button variant="outline" size="sm" onClick={() => setRefresh((value) => value + 1)}>
            Обновить
          </Button>
        </div>
      </CardHeader>
      <CardContent className="space-y-2">
        <p className="text-sm text-muted-foreground">
          Сохранённые чаты проекта. Вызовы CLI и несохранённые ответы агента не учитываются.
        </p>
        {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
        {!usage && !error && <p role="status" className="text-sm">Загружаем метрики агента.</p>}
        {usage && (
          <dl className="grid gap-3 text-sm sm:grid-cols-3">
            <div>
              <dt>Запросы</dt>
              <dd className="font-semibold tabular-nums">{usage.requests}</dd>
            </div>
            <div>
              <dt>Ответы агента</dt>
              <dd className="font-semibold tabular-nums">{usage.replies}</dd>
            </div>
            <div>
              <dt>Вызовы инструментов</dt>
              <dd className="font-semibold tabular-nums">{usage.tool_calls}</dd>
            </div>
          </dl>
        )}
      </CardContent>
    </Card>
  );
}
