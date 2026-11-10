import { suggestAssignee } from "@/lib/assigneeSuggestions";

type Props = {
  members: { id: number; display_name: string; email: string; role: string }[];
  tasks: { status: string; assignee_id?: number | null }[];
  ready: boolean;
};

export function AssigneeSuggestion({ members, tasks, ready }: Props) {
  if (!ready) {
    return <p className="text-xs text-muted-foreground">Загрузка рекомендации...</p>;
  }
  const suggestion = suggestAssignee(members, tasks);
  if (suggestion === null) {
    return <p className="text-xs text-muted-foreground">Нет участников с правом редактирования для рекомендации.</p>;
  }
  const name = suggestion.member.display_name || suggestion.member.email || `#${suggestion.member.id}`;
  return (
    <div aria-label="Рекомендация исполнителя" className="space-y-1 text-xs text-muted-foreground">
      <p>Рекомендуем: {name} · Незавершённых задач: {suggestion.workload}</p>
      <p>Меньше всего незавершённых задач среди участников с правом редактирования. При равенстве выбран участник с меньшим ID.</p>
    </div>
  );
}
