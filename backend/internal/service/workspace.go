package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"github.com/go-sql-driver/mysql"
	"golang.org/x/crypto/bcrypt"
	"novelstudio/internal/authority"
	"novelstudio/internal/domain"
	"strings"
	"time"
	"unicode/utf8"
)

type Workspace struct{ DB *sql.DB }

func NewID() string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		panic(err)
	}
	return hex.EncodeToString(value)
}
func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func validName(value string, max int) bool {
	return strings.TrimSpace(value) != "" && utf8.RuneCountInString(value) <= max
}
func validPassword(password string) bool { return len(password) >= 12 && len(password) <= 72 }
func databaseError(err error) error {
	var e *mysql.MySQLError
	if errors.As(err, &e) && e.Number == 1062 {
		return domain.ErrConflict
	}
	return err
}

func (s *Workspace) Setup(ctx context.Context, username, password, name string) error {
	username = strings.TrimSpace(username)
	name = strings.TrimSpace(name)
	if !validName(username, 80) || !validName(name, 120) || !validPassword(password) {
		return domain.ErrValidation
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err = tx.QueryRowContext(ctx, "SELECT version FROM schema_version WHERE id=1 FOR UPDATE").Scan(&version); err != nil {
		return err
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return domain.ErrConflict
	}
	workspace, id := NewID(), NewID()
	if _, err = tx.ExecContext(ctx, "INSERT INTO workspaces(id,name) VALUES(?,?)", workspace, name); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO users(id,workspace_id,username,password_hash,role) VALUES(?,?,?,?,'owner')", id, workspace, username, hash); err != nil {
		return databaseError(err)
	}
	return tx.Commit()
}
func (s *Workspace) Login(ctx context.Context, username, password string) (string, error) {
	var id, hash string
	err := s.DB.QueryRowContext(ctx, "SELECT id,password_hash FROM users WHERE username=? AND enabled=TRUE", strings.TrimSpace(username)).Scan(&id, &hash)
	if err != nil || bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return "", domain.ErrUnauthorized
	}
	token := rand.Text() + rand.Text()
	_, err = s.DB.ExecContext(ctx, "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES(?,?,?)", digest(token), id, time.Now().UTC().Add(12*time.Hour))
	return token, err
}
func (s *Workspace) Actor(ctx context.Context, token string) (domain.Actor, error) {
	var actor domain.Actor
	err := s.DB.QueryRowContext(ctx, `SELECT u.id,u.workspace_id,u.username,u.role FROM sessions s JOIN users u ON s.user_id=u.id WHERE s.token_hash=? AND s.expires_at>UTC_TIMESTAMP(6) AND u.enabled=TRUE`, digest(token)).Scan(&actor.ID, &actor.WorkspaceID, &actor.Username, &actor.Role)
	if err == sql.ErrNoRows {
		return actor, domain.ErrUnauthorized
	}
	return actor, err
}
func (s *Workspace) Logout(ctx context.Context, token string) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash=?", digest(token))
	return err
}
func (s *Workspace) actorTx(ctx context.Context, tx *sql.Tx, id string) (domain.Actor, error) {
	var a domain.Actor
	err := tx.QueryRowContext(ctx, "SELECT id,workspace_id,username,role FROM users WHERE id=? AND enabled=TRUE FOR UPDATE", id).Scan(&a.ID, &a.WorkspaceID, &a.Username, &a.Role)
	if err == sql.ErrNoRows {
		return a, domain.ErrForbidden
	}
	return a, err
}
func audit(ctx context.Context, tx *sql.Tx, a domain.Actor, action, id string) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO audit_events(workspace_id,actor_id,action,resource_id) VALUES(?,?,?,?)", a.WorkspaceID, a.ID, action, id)
	return err
}

func (s *Workspace) CreateProject(ctx context.Context, actorID, name, operation string) (domain.Project, error) {
	name = strings.TrimSpace(name)
	var p domain.Project
	if !validName(name, 120) || !validName(operation, 120) {
		return p, domain.ErrValidation
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return p, err
	}
	defer tx.Rollback()
	a, err := s.actorTx(ctx, tx, actorID)
	if err != nil {
		return p, err
	}
	if !domain.CanEdit(a.Role) {
		return p, domain.ErrForbidden
	}
	requestHash := digest("project.create\x00" + name)
	var stored, result string
	stored, result, err = operationReceiptTx(ctx, tx, a, operation)
	if err == nil {
		if stored != requestHash {
			return p, domain.ErrConflict
		}
		p, err = projectTx(ctx, tx, a, result)
		return p, err
	}
	if err != sql.ErrNoRows {
		return p, err
	}
	p = domain.Project{ID: NewID(), Name: name, Revision: 1, Role: "owner"}
	if _, err = tx.ExecContext(ctx, "INSERT INTO projects(id,workspace_id,name) VALUES(?,?,?)", p.ID, a.WorkspaceID, name); err != nil {
		return p, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO project_members(project_id,user_id,role) VALUES(?,?,'owner')", p.ID, a.ID); err != nil {
		return p, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO operations(actor_id,operation_id,kind,request_hash,result_id) VALUES(?,?,'project.create',?,?)", a.ID, operation, requestHash, p.ID); err != nil {
		return p, err
	}
	if err = audit(ctx, tx, a, "project.create", p.ID); err != nil {
		return p, err
	}
	if err = protectedLedger().AppendTx(ctx, tx, authority.Event{Kind: authority.ProjectCreated, ProjectID: p.ID, ResourceID: p.ID, ActorID: a.ID}); err != nil {
		return p, err
	}
	if err = captureMetadataSourceTx(ctx, tx, p.ID, "projects", p.ID); err != nil {
		return p, err
	}
	return p, tx.Commit()
}
func (s *Workspace) Projects(ctx context.Context, a domain.Actor) ([]domain.Project, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT p.id,p.name,p.revision,IF(?='owner','owner',m.role) FROM projects p LEFT JOIN project_members m ON m.project_id=p.id AND m.user_id=? WHERE p.workspace_id=? AND p.deleted_at IS NULL AND (?='owner' OR m.user_id IS NOT NULL) ORDER BY p.created_at,p.id`, a.Role, a.ID, a.WorkspaceID, a.Role)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []domain.Project{}
	for rows.Next() {
		var p domain.Project
		if err = rows.Scan(&p.ID, &p.Name, &p.Revision, &p.Role); err != nil {
			return nil, err
		}
		items = append(items, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	visible := []domain.Project{}
	for _, p := range items {
		active, err := protectedLedger().ProjectActiveTx(ctx, tx, p.ID)
		if err != nil {
			return nil, err
		}
		if active {
			visible = append(visible, p)
		}
	}
	return visible, nil
}
func (s *Workspace) Project(ctx context.Context, a domain.Actor, id string) (domain.Project, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return domain.Project{}, err
	}
	defer tx.Rollback()
	return projectTx(ctx, tx, a, id)
}
func (s *Workspace) UpdateProject(ctx context.Context, actorID, id, name string, revision int64) (domain.Project, error) {
	var p domain.Project
	name = strings.TrimSpace(name)
	if !validName(name, 120) || revision < 1 {
		return p, domain.ErrValidation
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return p, err
	}
	defer tx.Rollback()
	a, err := s.actorTx(ctx, tx, actorID)
	if err != nil {
		return p, err
	}
	p, err = projectTx(ctx, tx, a, id)
	if err != nil {
		return p, err
	}
	if !domain.CanEdit(p.Role) || !domain.CanEdit(a.Role) {
		return p, domain.ErrForbidden
	}
	if p.Revision != revision {
		return p, domain.ErrConflict
	}
	if err = captureMetadataSourceTx(ctx, tx, id, "projects", id); err != nil {
		return p, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE projects SET name=?,revision=revision+1 WHERE id=?", name, id); err != nil {
		return p, err
	}
	if err = audit(ctx, tx, a, "project.rename", id); err != nil {
		return p, err
	}
	if err = captureMetadataSourceTx(ctx, tx, id, "projects", id); err != nil {
		return p, err
	}
	p.Name = name
	p.Revision++
	return p, tx.Commit()
}
func projectTx(ctx context.Context, tx *sql.Tx, a domain.Actor, id string) (domain.Project, error) {
	var p domain.Project
	err := tx.QueryRowContext(ctx, "SELECT id,name,revision FROM projects WHERE id=? AND workspace_id=? AND deleted_at IS NULL FOR UPDATE", id, a.WorkspaceID).Scan(&p.ID, &p.Name, &p.Revision)
	if err == sql.ErrNoRows {
		return p, domain.ErrForbidden
	}
	if err != nil {
		return p, err
	}
	active, err := protectedLedger().ProjectActiveTx(ctx, tx, id)
	if err != nil {
		return p, err
	}
	if !active {
		return p, domain.ErrForbidden
	}
	if a.Role == "owner" {
		p.Role = "owner"
		return p, nil
	}
	err = tx.QueryRowContext(ctx, "SELECT role FROM project_members WHERE project_id=? AND user_id=?", id, a.ID).Scan(&p.Role)
	if err == sql.ErrNoRows {
		return p, domain.ErrForbidden
	}
	return p, err
}
func (s *Workspace) CreateMember(ctx context.Context, actorID, username, password, role string) (domain.Actor, error) {
	var member domain.Actor
	if !validName(username, 80) || !validPassword(password) || !domain.ValidRole(role) || role == "owner" {
		return member, domain.ErrValidation
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return member, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return member, err
	}
	defer tx.Rollback()
	a, err := s.actorTx(ctx, tx, actorID)
	if err != nil {
		return member, err
	}
	if a.Role != "owner" {
		return member, domain.ErrForbidden
	}
	member = domain.Actor{ID: NewID(), WorkspaceID: a.WorkspaceID, Username: strings.TrimSpace(username), Role: role}
	_, err = tx.ExecContext(ctx, "INSERT INTO users(id,workspace_id,username,password_hash,role) VALUES(?,?,?,?,?)", member.ID, member.WorkspaceID, member.Username, hash, role)
	if err != nil {
		return member, databaseError(err)
	}
	if err = audit(ctx, tx, a, "member.create", member.ID); err != nil {
		return member, err
	}
	return member, tx.Commit()
}
func (s *Workspace) Members(ctx context.Context, a domain.Actor) ([]domain.Actor, error) {
	if a.Role != "owner" {
		return nil, domain.ErrForbidden
	}
	rows, err := s.DB.QueryContext(ctx, "SELECT id,workspace_id,username,role FROM users WHERE workspace_id=? AND enabled=TRUE ORDER BY created_at", a.WorkspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.Actor{}
	for rows.Next() {
		var m domain.Actor
		if err = rows.Scan(&m.ID, &m.WorkspaceID, &m.Username, &m.Role); err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	return result, rows.Err()
}
func (s *Workspace) SetMember(ctx context.Context, actorID, project, user, role string, remove bool) error {
	if !remove && (!domain.ValidRole(role) || role == "owner") {
		return domain.ErrValidation
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	a, err := s.actorTx(ctx, tx, actorID)
	if err != nil {
		return err
	}
	if a.Role != "owner" {
		return domain.ErrForbidden
	}
	if _, err = projectTx(ctx, tx, a, project); err != nil {
		return err
	}
	var workspace, memberRole string
	if err = tx.QueryRowContext(ctx, "SELECT workspace_id,role FROM users WHERE id=? AND enabled=TRUE", user).Scan(&workspace, &memberRole); err != nil {
		return domain.ErrForbidden
	}
	if workspace != a.WorkspaceID || memberRole == "owner" {
		return domain.ErrForbidden
	}
	if remove {
		_, err = tx.ExecContext(ctx, "DELETE FROM project_members WHERE project_id=? AND user_id=?", project, user)
	} else {
		_, err = tx.ExecContext(ctx, "INSERT INTO project_members(project_id,user_id,role) VALUES(?,?,?) ON DUPLICATE KEY UPDATE role=VALUES(role)", project, user, role)
	}
	if err != nil {
		return err
	}
	if err = audit(ctx, tx, a, "project.membership", project); err != nil {
		return err
	}
	return tx.Commit()
}
