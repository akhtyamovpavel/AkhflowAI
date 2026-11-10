import { useEffect, useState } from "react";
import { Button } from "./ui/button";
import { Input } from "./ui/input";

type Request = (path: string, options?: RequestInit, token?: string | null) => Promise<any>;
type Member = { id: number; email: string; display_name: string; role: string };

export function ProjectMembers({ project, token, request, onMembersChanged }: { project: string; token: string; request: Request; onMembersChanged: () => Promise<void> }) {
  const [role, setRole] = useState("");
  const [members, setMembers] = useState<Member[]>([]);
  const [open, setOpen] = useState(false);
  const [email, setEmail] = useState("");
  const [selectedRole, setSelectedRole] = useState("viewer");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");

  useEffect(() => {
    let active = true;
    request(`/api/projects/${project}/role`, {}, token)
      .then((result: { role: string }) => {
        if (active) setRole(result.role);
      })
      .catch(() => {
        if (active) setMessage("Не удалось загрузить права проекта");
      });
    return () => { active = false; };
  }, [project, token, request]);

  const loadMembers = async () => {
    const data: Member[] = await request(`/api/projects/${project}/members`, {}, token);
    setMembers(data);
  };

  const toggle = async () => {
    if (open) {
      setOpen(false);
      return;
    }
    try {
      await loadMembers();
      setOpen(true);
      setMessage("");
    } catch {
      setMessage("Не удалось загрузить участников");
    }
  };

  const save = async () => {
    if (!email.trim() || busy) return;
    setBusy(true);
    setMessage("");
    try {
      await request(`/api/projects/${project}/members`, {
        method: "PUT",
        body: JSON.stringify({ email: email.trim(), role: selectedRole }),
      }, token);
      await Promise.all([loadMembers(), onMembersChanged()]);
      setEmail("");
      setMessage("Права участника сохранены");
    } catch {
      setMessage("Не удалось сохранить права. Проверь email зарегистрированного участника.");
    } finally {
      setBusy(false);
    }
  };

  const canManage = role === "owner" || role === "admin";
  return (
    <section className="space-y-2" aria-label="Права проекта">
      {role === "viewer" && <p className="text-sm">Доступ к проекту: только просмотр.</p>}
      {canManage && (
        <>
          <Button variant="outline" size="sm" onClick={toggle} aria-expanded={open}>Участники и роли</Button>
          {open && (
            <div className="max-w-xl space-y-2 rounded-md border p-3">
              <ul className="space-y-1 text-sm">
                {members.map((member) => <li key={member.id}>{member.display_name} ({member.email}) — {member.role}</li>)}
              </ul>
              <label htmlFor="member-email" className="text-sm">Email зарегистрированного пользователя</label>
              <Input id="member-email" type="email" value={email} onChange={(event) => setEmail(event.target.value)} />
              <label htmlFor="member-role" className="text-sm">Роль</label>
              <select id="member-role" value={selectedRole} onChange={(event) => setSelectedRole(event.target.value)}>
                <option value="viewer">Просмотр</option>
                <option value="editor">Редактирование</option>
              </select>
              <Button size="sm" disabled={busy || !email.trim()} onClick={save}>Сохранить права</Button>
            </div>
          )}
        </>
      )}
      {message && <p role="status" className="text-sm">{message}</p>}
    </section>
  );
}
