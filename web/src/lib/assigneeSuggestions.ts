type Member = {
  id: number;
  display_name: string;
  email: string;
  role: string;
};

type Task = {
  status: string;
  assignee_id?: number | null;
};

export function suggestAssignee(members: Member[], tasks: Task[]) {
  const eligibleMembers = members.filter((member) => ["owner", "admin", "member", "editor"].includes(member.role));
  const counts = new Map<number, number>();
  for (const member of eligibleMembers) {
    counts.set(member.id, 0);
  }
  for (const task of tasks) {
    if (task.status === "done" || task.assignee_id == null) {
      continue;
    }
    const current = counts.get(task.assignee_id);
    if (current !== undefined) {
      counts.set(task.assignee_id, current + 1);
    }
  }
  let candidate: Member | null = null;
  let workload = 0;
  for (const member of eligibleMembers) {
    const count = counts.get(member.id) ?? 0;
    if (candidate === null || count < workload || (count === workload && member.id < candidate.id)) {
      candidate = member;
      workload = count;
    }
  }
  if (candidate === null) {
    return null;
  }
  return { member: candidate, workload };
}
