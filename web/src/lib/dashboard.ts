export type DashboardTask = {
  status: string;
  priority: string;
  assignee_id?: number | null;
  epic_id?: number | null;
};

export type DashboardEpic = {
  id: number;
  title: string;
};

export type DashboardMember = {
  id: number;
  display_name: string;
};

export type DashboardRow = {
  id: string;
  label: string;
  count: number;
};

const STATUSES = [
  { id: "backlog", label: "Backlog" },
  { id: "todo", label: "Todo" },
  { id: "in_progress", label: "In Progress" },
  { id: "done", label: "Done" },
];

const PRIORITIES = [
  { id: "high", label: "Высокий" },
  { id: "medium", label: "Средний" },
  { id: "low", label: "Низкий" },
];

export function summarizeEpics(tasks: DashboardTask[], epics: DashboardEpic[]) {
  const counts = new Map<number, { total: number; done: number }>();
  for (const epic of epics) {
    counts.set(epic.id, { total: 0, done: 0 });
  }
  for (const task of tasks) {
    if (task.epic_id == null) {
      continue;
    }
    const count = counts.get(task.epic_id);
    if (!count) {
      continue;
    }
    count.total++;
    if (task.status === "done") {
      count.done++;
    }
  }
  return epics.map((epic) => {
    const count = counts.get(epic.id)!;
    let percent = 0;
    if (count.total > 0) {
      percent = Math.round((count.done / count.total) * 100);
    }
    return { ...epic, ...count, percent };
  });
}

function countBy(
  tasks: DashboardTask[],
  field: "status" | "priority",
  categories: { id: string; label: string }[],
): DashboardRow[] {
  const counts = new Map<string, number>();
  for (const task of tasks) {
    counts.set(task[field], (counts.get(task[field]) ?? 0) + 1);
  }
  const rows = categories.map((category) => ({
    ...category,
    count: counts.get(category.id) ?? 0,
  }));
  const known = new Set(categories.map((category) => category.id));
  for (const [id, count] of counts) {
    if (!known.has(id)) {
      rows.push({ id, label: id || "Не указан", count });
    }
  }
  return rows;
}

export function summarizeDashboard(tasks: DashboardTask[], members: DashboardMember[]) {
  const assigned = new Map<number, number>();
  let unassigned = 0;
  for (const task of tasks) {
    if (task.assignee_id == null) {
      unassigned++;
    } else {
      assigned.set(task.assignee_id, (assigned.get(task.assignee_id) ?? 0) + 1);
    }
  }
  const memberIds = new Set(members.map((member) => member.id));
  const assignees: DashboardRow[] = members.map((member) => ({
    id: String(member.id),
    label: member.display_name || `Пользователь #${member.id}`,
    count: assigned.get(member.id) ?? 0,
  }));
  for (const [id, count] of assigned) {
    if (!memberIds.has(id)) {
      assignees.push({ id: String(id), label: `Пользователь #${id}`, count });
    }
  }
  assignees.push({ id: "unassigned", label: "Без исполнителя", count: unassigned });
  return {
    total: tasks.length,
    statuses: countBy(tasks, "status", STATUSES),
    priorities: countBy(tasks, "priority", PRIORITIES),
    assignees,
  };
}
