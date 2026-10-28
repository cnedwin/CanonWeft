package domain

import "errors"

var (
	ErrForbidden       = errors.New("没有此操作的权限")
	ErrUnauthorized    = errors.New("请先登录，或重新登录")
	ErrConflict        = errors.New("内容已更新或操作与原请求不一致，请刷新后重试")
	ErrValidation      = errors.New("输入不完整或不符合要求")
	ErrNotReady        = errors.New("运行环境尚未就绪")
	ErrMaterialExpired = errors.New("运行材料已到保留期限，不能继续执行；请重新准备任务。已记录费用不会重新结算")
	ErrNewerSchema     = errors.New("数据库版本高于当前程序，已停止写入")
)

type Scope struct{ WorkspaceID, ProjectID, ActorID string }
type VersionGuard struct {
	ExpectedHead   string
	Generation     int64
	PolicyRevision string
}
type Operation struct {
	ID    string
	Scope Scope
	Guard VersionGuard
}
type Ref struct{ ID, Revision, Hash string }
type Actor struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Username    string `json:"username"`
	Role        string `json:"role"`
}
type Project struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Revision int64  `json:"revision"`
	Role     string `json:"role"`
}

func ValidRole(role string) bool {
	return role == "owner" || role == "editor" || role == "publisher" || role == "reader"
}
func CanEdit(role string) bool { return role == "owner" || role == "editor" }
