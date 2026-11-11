package store

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"akhflow.ai/akhflow/internal/domain"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations
var migrationsFS embed.FS

var ErrNotFound = errors.New("not found")
var ErrUserExists = errors.New("user already exists")

type Store struct {
	db *pgxpool.Pool
}

func New(dsn string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		return nil, fmt.Errorf("pool: %w", err)
	}
	return &Store{db: pool}, nil
}

func (s *Store) Close() {
	s.db.Close()
}

func (s *Store) Ping(ctx context.Context) error {
	return s.db.Ping(ctx)
}

func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.db.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		name TEXT PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("schema_migrations: %w", err)
	}
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		var applied bool
		err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name=$1)`, name).Scan(&applied)
		if err != nil {
			return err
		}
		if applied {
			continue
		}
		body, err := fs.ReadFile(migrationsFS, "migrations/"+name)
		if err != nil {
			return err
		}
		tx, err := s.db.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(body)); err != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations(name) VALUES($1)`, name); err != nil {
			tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

// ---------- users ----------

func (s *Store) CreateUser(ctx context.Context, email, displayName, passwordHash string) (int64, error) {
	var id int64
	query := `INSERT INTO users(email, display_name, password_hash) VALUES($1,$2,$3) ON CONFLICT (email) DO NOTHING RETURNING id`
	err := s.db.QueryRow(ctx, query, email, displayName, passwordHash).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrUserExists
	}
	return id, err
}

func (s *Store) UserByID(ctx context.Context, id int64) (*domain.User, error) {
	u, err := s.scanUser(s.db.QueryRow(ctx, `SELECT id, email, display_name, created_at FROM users WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return u, err
}

func (s *Store) UserByEmail(ctx context.Context, email string) (*domain.User, error) {
	u, err := s.scanUser(s.db.QueryRow(ctx, `SELECT id, email, display_name, created_at FROM users WHERE email=$1`, email))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return u, err
}

func (s *Store) PasswordHashByEmail(ctx context.Context, email string) (string, error) {
	var hash string
	err := s.db.QueryRow(ctx, `SELECT password_hash FROM users WHERE email=$1`, email).Scan(&hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return hash, err
}

func (s *Store) ProjectsOfUser(ctx context.Context, userID int64) ([]domain.Project, error) {
	rows, err := s.db.Query(ctx, `
		SELECT p.id, p.slug, p.name, p.owner_id
		FROM projects p
		JOIN project_members pm ON pm.project_id = p.id
		WHERE pm.user_id = $1
		ORDER BY p.id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Project{}
	for rows.Next() {
		var p domain.Project
		if err := rows.Scan(&p.ID, &p.Slug, &p.Name, &p.OwnerID); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) scanUser(row pgx.Row) (*domain.User, error) {
	var u domain.User
	if err := row.Scan(&u.ID, &u.Email, &u.DisplayName, &u.CreatedAt); err != nil {
		return nil, err
	}
	return &u, nil
}

// ---------- projects ----------

func (s *Store) CreateProject(ctx context.Context, slug, name string, ownerID int64) (int64, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	var id int64
	if err := tx.QueryRow(ctx, `INSERT INTO projects(slug, name, owner_id) VALUES($1,$2,$3) RETURNING id`, slug, name, ownerID).Scan(&id); err != nil {
		tx.Rollback(ctx)
		return 0, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO project_members(project_id, user_id, role) VALUES($1,$2,'owner')`, id, ownerID); err != nil {
		tx.Rollback(ctx)
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return id, nil
}

func (s *Store) ProjectBySlug(ctx context.Context, slug string) (*domain.Project, error) {
	p, err := s.scanProject(s.db.QueryRow(ctx, `SELECT id, slug, name, owner_id FROM projects WHERE slug=$1`, slug))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

func (s *Store) ProjectByID(ctx context.Context, id int64) (*domain.Project, error) {
	p, err := s.scanProject(s.db.QueryRow(ctx, `SELECT id, slug, name, owner_id FROM projects WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

func (s *Store) IsMember(ctx context.Context, projectID, userID int64) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM project_members WHERE project_id=$1 AND user_id=$2)`, projectID, userID).Scan(&ok)
	return ok, err
}

func (s *Store) ProjectRole(ctx context.Context, projectID, userID int64) (string, error) {
	var role string
	err := s.db.QueryRow(ctx, `SELECT role FROM project_members WHERE project_id=$1 AND user_id=$2`, projectID, userID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return role, err
}

func (s *Store) SetProjectMemberRole(ctx context.Context, projectID, actorID, userID int64, role string) (bool, error) {
	var updated int64
	query := `INSERT INTO project_members(project_id, user_id, role)
		SELECT $1, $3, $4 WHERE EXISTS (
			SELECT 1 FROM project_members WHERE project_id=$1 AND user_id=$2 AND role IN ('owner','admin')
		) AND EXISTS (SELECT 1 FROM users WHERE id=$3)
		AND NOT EXISTS (SELECT 1 FROM projects WHERE id=$1 AND owner_id=$3)
		ON CONFLICT (project_id, user_id) DO UPDATE SET role=EXCLUDED.role
		WHERE project_members.role <> 'owner'
		RETURNING user_id`
	err := s.db.QueryRow(ctx, query, projectID, actorID, userID, role).Scan(&updated)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

var ErrAssigneeNotMember = errors.New("assignee is not a project member")

func (s *Store) ValidateAssignee(ctx context.Context, projectID, userID int64) error {
	member, err := s.IsMember(ctx, projectID, userID)
	if err != nil {
		return err
	}
	if !member {
		return ErrAssigneeNotMember
	}
	return nil
}

func (s *Store) ListProjectMembers(ctx context.Context, projectID int64) ([]domain.User, error) {
	rows, err := s.db.Query(ctx, `
		SELECT u.id, u.email, u.display_name, pm.role
		FROM project_members pm
		JOIN users u ON u.id = pm.user_id
		WHERE pm.project_id = $1
		ORDER BY u.id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.User{}
	for rows.Next() {
		var u domain.User
		if err := rows.Scan(&u.ID, &u.Email, &u.DisplayName, &u.Role); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) scanProject(row pgx.Row) (*domain.Project, error) {
	var p domain.Project
	if err := row.Scan(&p.ID, &p.Slug, &p.Name, &p.OwnerID); err != nil {
		return nil, err
	}
	return &p, nil
}

// ---------- tasks ----------

const taskCols = `id, project_id, parent_id, title, description, status, priority, estimate_min, due_date, author_id, assignee_id, recurring, epic_id, created_at, updated_at`

func (s *Store) CreateTask(ctx context.Context, t domain.Task) (int64, error) {
	var id int64
	err := s.db.QueryRow(ctx, `
		INSERT INTO tasks(project_id, parent_id, title, description, status, priority, estimate_min, due_date, author_id, assignee_id, recurring, epic_id)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING id`,
		t.ProjectID, t.ParentID, t.Title, t.Description, t.Status, t.Priority, t.EstimateMin, t.DueDate, t.AuthorID, t.AssigneeID, t.Recurring, t.EpicID).Scan(&id)
	if err != nil {
		return 0, err
	}
	for _, lbl := range t.Labels {
		if _, err := s.db.Exec(ctx, `INSERT INTO task_labels(task_id, label) VALUES($1,$2) ON CONFLICT DO NOTHING`, id, lbl); err != nil {
			return 0, err
		}
	}
	return id, nil
}

func (s *Store) TaskByID(ctx context.Context, id int64) (*domain.Task, error) {
	t, err := s.scanTask(s.db.QueryRow(ctx, `SELECT `+taskCols+` FROM tasks WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := s.hydrateTask(ctx, t); err != nil {
		return nil, err
	}
	return t, nil
}

func (s *Store) ListTasks(ctx context.Context, projectID int64, status string) ([]domain.Task, error) {
	// recurring masters are templates, not workflow items
	q := `SELECT ` + taskCols + ` FROM tasks WHERE project_id=$1 AND recurring=''`
	args := []any{projectID}
	if status != "" {
		q += ` AND status=$2`
		args = append(args, status)
	}
	q += ` ORDER BY id DESC`
	return s.queryTasks(ctx, q, args...)
}

func (s *Store) ListTasksByEpic(ctx context.Context, epicID int64) ([]domain.Task, error) {
	return s.queryTasks(ctx, `SELECT `+taskCols+` FROM tasks WHERE epic_id=$1 AND recurring='' ORDER BY id DESC`, epicID)
}

func (s *Store) ListRecurringOverdue(ctx context.Context, now time.Time) ([]domain.Task, error) {
	return s.queryTasks(ctx, `SELECT `+taskCols+` FROM tasks
		WHERE recurring != '' AND due_date IS NOT NULL AND due_date <= $1::date AND status != $2
		ORDER BY due_date, id`, now.Format(time.DateOnly), domain.StatusDone)
}

func (s *Store) UpdateTask(ctx context.Context, id int64, sets []string, args []any) error {
	if len(sets) == 0 {
		return nil
	}
	setClauses := make([]string, 0, len(sets))
	for i, col := range sets {
		setClauses = append(setClauses, fmt.Sprintf("%s=$%d", col, i+1))
	}
	params := make([]any, 0, len(args)+1)
	params = append(params, args...)
	params = append(params, id)
	query := fmt.Sprintf(`UPDATE tasks SET %s, updated_at=now() WHERE id=$%d RETURNING id`, strings.Join(setClauses, ", "), len(params))
	var updatedID int64
	if err := s.db.QueryRow(ctx, query, params...).Scan(&updatedID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

// UpdateTaskWithStatusChange compares the status while holding the row lock.
func (s *Store) UpdateTaskWithStatusChange(ctx context.Context, id int64, sets []string, args []any) (bool, error) {
	result, err := s.UpdateTaskWithRecurrence(ctx, id, sets, args, "system", 0)
	return result.StatusChanged, err
}

func (s *Store) queryTasks(ctx context.Context, q string, args ...any) ([]domain.Task, error) {
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Task{}
	for rows.Next() {
		t, err := s.scanTask(rows)
		if err != nil {
			return nil, err
		}
		if err := s.hydrateTask(ctx, t); err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func (s *Store) scanTask(row pgx.Row) (*domain.Task, error) {
	var t domain.Task
	if err := row.Scan(&t.ID, &t.ProjectID, &t.ParentID, &t.Title, &t.Description, &t.Status, &t.Priority, &t.EstimateMin, &t.DueDate, &t.AuthorID, &t.AssigneeID, &t.Recurring, &t.EpicID, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return nil, err
	}
	return &t, nil
}

type taskHydrator interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (s *Store) hydrateTask(ctx context.Context, t *domain.Task) error {
	return hydrateTask(ctx, s.db, t)
}

func hydrateTask(ctx context.Context, db taskHydrator, t *domain.Task) error {
	rows, err := db.Query(ctx, `SELECT label FROM task_labels WHERE task_id=$1 ORDER BY label`, t.ID)
	if err != nil {
		return err
	}
	t.Labels = []string{}
	for rows.Next() {
		var l string
		if err := rows.Scan(&l); err != nil {
			rows.Close()
			return err
		}
		t.Labels = append(t.Labels, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	rows, err = db.Query(ctx, `SELECT depends_on FROM task_dependencies WHERE task_id=$1 ORDER BY depends_on`, t.ID)
	if err != nil {
		return err
	}
	t.DependsOn = []int64{}
	for rows.Next() {
		var d int64
		if err := rows.Scan(&d); err != nil {
			rows.Close()
			return err
		}
		t.DependsOn = append(t.DependsOn, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	rows, err = db.Query(ctx, `SELECT task_id FROM task_dependencies WHERE depends_on=$1 ORDER BY task_id`, t.ID)
	if err != nil {
		return err
	}
	t.Blocks = []int64{}
	for rows.Next() {
		var d int64
		if err := rows.Scan(&d); err != nil {
			rows.Close()
			return err
		}
		t.Blocks = append(t.Blocks, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	var att domain.Attachment
	err = db.QueryRow(ctx, `SELECT id, task_id, filename, size_bytes, storage_key, created_at FROM attachments WHERE task_id=$1 ORDER BY id LIMIT 1`, t.ID).
		Scan(&att.ID, &att.TaskID, &att.Filename, &att.SizeBytes, &att.StorageKey, &att.CreatedAt)
	if err == nil {
		t.Attachment = &att
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	return nil
}

// ---------- comments ----------

// TaskBlockedError contains the tasks that prevented deletion.
type TaskBlockedError struct {
	Dependents []domain.Task
}

func (e *TaskBlockedError) Error() string { return "task blocks other tasks" }

// DeleteTask serializes the dependency check and deletion with AddDependency.
func (s *Store) DeleteTask(ctx context.Context, id int64) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var projectID int64
	err = tx.QueryRow(ctx, `SELECT project_id FROM tasks WHERE id=$1`, id).Scan(&projectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	err = tx.QueryRow(ctx, `SELECT id FROM projects WHERE id=$1 FOR NO KEY UPDATE`, projectID).Scan(&projectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tasks WHERE id=$1 AND project_id=$2)`, id, projectID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	rows, err := tx.Query(ctx, `SELECT `+taskCols+` FROM tasks WHERE id IN (SELECT task_id FROM task_dependencies WHERE depends_on=$1) ORDER BY id`, id)
	if err != nil {
		return err
	}
	dependents := []domain.Task{}
	for rows.Next() {
		task, err := s.scanTask(rows)
		if err != nil {
			rows.Close()
			return err
		}
		dependents = append(dependents, *task)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(dependents) > 0 {
		for i := range dependents {
			if err := hydrateTask(ctx, tx, &dependents[i]); err != nil {
				return err
			}
		}
		return &TaskBlockedError{Dependents: dependents}
	}
	tag, err := tx.Exec(ctx, `DELETE FROM tasks WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return tx.Commit(ctx)
}

func (s *Store) AddComment(ctx context.Context, taskID, authorID int64, body string) (int64, error) {
	var id int64
	err := s.db.QueryRow(ctx, `INSERT INTO comments(task_id, author_id, body) VALUES($1,$2,$3) RETURNING id`, taskID, authorID, body).Scan(&id)
	return id, err
}

func (s *Store) ListComments(ctx context.Context, taskID int64) ([]domain.Comment, error) {
	rows, err := s.db.Query(ctx, `SELECT id, task_id, author_id, body, created_at FROM comments WHERE task_id=$1 ORDER BY id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Comment{}
	for rows.Next() {
		var c domain.Comment
		if err := rows.Scan(&c.ID, &c.TaskID, &c.AuthorID, &c.Body, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ---------- dependencies ----------

var ErrDependencyCycle = errors.New("dependency would create a cycle")
var ErrDependencyDifferentProject = errors.New("dependency must be in the same project")

// AddDependency serializes graph changes per project across all API and MCP processes.
func (s *Store) AddDependency(ctx context.Context, taskID, dependsOn int64) error {
	if taskID == dependsOn {
		return ErrDependencyCycle
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var projectID int64
	err = tx.QueryRow(ctx, `SELECT project_id FROM tasks WHERE id=$1`, taskID).Scan(&projectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	// NO KEY UPDATE conflicts with other dependency insertions but not with
	// ordinary foreign-key checks on the project row.
	err = tx.QueryRow(ctx, `SELECT id FROM projects WHERE id=$1 FOR NO KEY UPDATE`, projectID).Scan(&projectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	var sourceProject, targetProject int64
	err = tx.QueryRow(ctx, `SELECT source.project_id, target.project_id FROM tasks source JOIN tasks target ON target.id=$2 WHERE source.id=$1`, taskID, dependsOn).Scan(&sourceProject, &targetProject)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if sourceProject != projectID || targetProject != projectID {
		return ErrDependencyDifferentProject
	}
	var existing bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM task_dependencies WHERE task_id=$1 AND depends_on=$2)`, taskID, dependsOn).Scan(&existing)
	if err != nil {
		return err
	}
	if existing {
		return tx.Commit(ctx)
	}
	var cycle bool
	err = tx.QueryRow(ctx, `
		WITH RECURSIVE reachable AS (
			SELECT depends_on FROM task_dependencies WHERE task_id = $2
			UNION
			SELECT td.depends_on FROM task_dependencies td
			JOIN reachable r ON td.task_id = r.depends_on
		)
		SELECT EXISTS(SELECT 1 FROM reachable WHERE depends_on = $1)`, taskID, dependsOn).Scan(&cycle)
	if err != nil {
		return err
	}
	if cycle {
		return ErrDependencyCycle
	}
	_, err = tx.Exec(ctx, `INSERT INTO task_dependencies(task_id, depends_on) VALUES($1,$2) ON CONFLICT DO NOTHING`, taskID, dependsOn)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) RemoveDependency(ctx context.Context, taskID, dependsOn int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM task_dependencies WHERE task_id=$1 AND depends_on=$2`, taskID, dependsOn)
	return err
}

// UnfinishedDependencies returns the tasks that taskID depends on which are not done yet.
func (s *Store) UnfinishedDependencies(ctx context.Context, taskID int64) ([]domain.Task, error) {
	return s.queryTasks(ctx, `SELECT `+taskCols+` FROM tasks t
		JOIN task_dependencies td ON td.depends_on = t.id
		WHERE td.task_id = $1 AND t.status != $2
		ORDER BY t.id`, taskID, domain.StatusDone)
}

func (s *Store) DependenciesDetail(ctx context.Context, taskID int64) ([]domain.Task, error) {
	return s.queryTasks(ctx, `SELECT `+taskCols+` FROM tasks t
		JOIN task_dependencies td ON td.depends_on = t.id
		WHERE td.task_id = $1 ORDER BY t.id`, taskID)
}

func (s *Store) DependentsDetail(ctx context.Context, taskID int64) ([]domain.Task, error) {
	return s.queryTasks(ctx, `SELECT `+taskCols+` FROM tasks t
		JOIN task_dependencies td ON td.task_id = t.id
		WHERE td.depends_on = $1 ORDER BY t.id`, taskID)
}

// ---------- epics ----------

func (s *Store) CreateEpic(ctx context.Context, projectID int64, title, description, status string) (int64, error) {
	var id int64
	err := s.db.QueryRow(ctx, `INSERT INTO epics(project_id, title, description, status) VALUES($1,$2,$3,$4) RETURNING id`, projectID, title, description, status).Scan(&id)
	return id, err
}

func (s *Store) EpicByID(ctx context.Context, id int64) (*domain.Epic, error) {
	var e domain.Epic
	err := s.db.QueryRow(ctx, `SELECT id, project_id, title, description, status, created_at FROM epics WHERE id=$1`, id).
		Scan(&e.ID, &e.ProjectID, &e.Title, &e.Description, &e.Status, &e.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func (s *Store) EpicsOfProject(ctx context.Context, projectID int64) ([]domain.Epic, error) {
	rows, err := s.db.Query(ctx, `SELECT id, project_id, title, description, status, created_at FROM epics WHERE project_id=$1 ORDER BY id DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Epic{}
	for rows.Next() {
		var e domain.Epic
		if err := rows.Scan(&e.ID, &e.ProjectID, &e.Title, &e.Description, &e.Status, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) DeleteEpic(ctx context.Context, id int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM epics WHERE id=$1`, id)
	return err
}

// ---------- agent chats ----------

func (s *Store) CreateChat(ctx context.Context, projectID, userID int64) (int64, error) {
	var id int64
	err := s.db.QueryRow(ctx, `INSERT INTO agent_chats(project_id, user_id) VALUES($1,$2) RETURNING id`, projectID, userID).Scan(&id)
	return id, err
}

func (s *Store) ChatByID(ctx context.Context, id int64) (*domain.Chat, error) {
	var c domain.Chat
	err := s.db.QueryRow(ctx, `SELECT id, project_id, user_id, title, created_at FROM agent_chats WHERE id=$1`, id).
		Scan(&c.ID, &c.ProjectID, &c.UserID, &c.Title, &c.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *Store) ChatsOfProject(ctx context.Context, projectID int64) ([]domain.Chat, error) {
	rows, err := s.db.Query(ctx, `SELECT id, project_id, user_id, title, created_at FROM agent_chats WHERE project_id=$1 ORDER BY id DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Chat{}
	for rows.Next() {
		var c domain.Chat
		if err := rows.Scan(&c.ID, &c.ProjectID, &c.UserID, &c.Title, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) MessagesOfChat(ctx context.Context, chatID int64) ([]domain.AgentMessage, error) {
	rows, err := s.db.Query(ctx, `SELECT id, chat_id, role, content, tool_calls FROM agent_messages WHERE chat_id=$1 ORDER BY id`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.AgentMessage{}
	for rows.Next() {
		var m domain.AgentMessage
		if err := rows.Scan(&m.ID, &m.ChatID, &m.Role, &m.Content, &m.ToolCalls); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) AddAgentMessage(ctx context.Context, chatID int64, role, content string, toolCalls any) (int64, error) {
	var tc any
	if toolCalls != nil {
		b, err := json.Marshal(toolCalls)
		if err != nil {
			return 0, err
		}
		tc = b
	}
	var id int64
	err := s.db.QueryRow(ctx, `INSERT INTO agent_messages(chat_id, role, content, tool_calls) VALUES($1,$2,$3,$4) RETURNING id`, chatID, role, content, tc).Scan(&id)
	return id, err
}

func (s *Store) SetChatTitle(ctx context.Context, chatID int64, title string) error {
	_, err := s.db.Exec(ctx, `UPDATE agent_chats SET title=$1 WHERE id=$2`, title, chatID)
	return err
}

// ---------- audit ----------

func (s *Store) Audit(ctx context.Context, actor, action, entity string, entityID *int64, payload map[string]any) error {
	_, err := s.db.Exec(ctx, `INSERT INTO audit_log(actor, action, entity, entity_id, payload) VALUES($1,$2,$3,$4,$5)`, actor, action, entity, entityID, payload)
	return err
}

// AuditForTask returns the audit history of a task, newest first. The result is
// always initialized (never nil) so it marshals to JSON `[]`, not `null`.
func (s *Store) AuditForTask(ctx context.Context, taskID int64, limit int) ([]domain.AuditEntry, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(ctx, `
		SELECT id, actor, action, entity, entity_id, payload, created_at
		FROM audit_log
		WHERE entity = 'task' AND entity_id = $1
		ORDER BY id DESC
		LIMIT $2`, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.AuditEntry{}
	for rows.Next() {
		var e domain.AuditEntry
		var payload []byte // NULL and JSON `null` both scan to nil
		if err := rows.Scan(&e.ID, &e.Actor, &e.Action, &e.Entity, &e.EntityID, &payload, &e.CreatedAt); err != nil {
			return nil, err
		}
		if len(payload) > 0 {
			if err := json.Unmarshal(payload, &e.Payload); err != nil {
				return nil, fmt.Errorf("audit %d payload: %w", e.ID, err)
			}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
