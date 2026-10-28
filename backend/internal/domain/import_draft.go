package domain

import (
	"strings"
	"unicode/utf8"
)

type ImportDraftContent struct {
	Policy        *KnowledgePolicy `json:"policy,omitempty"`
	BaseIPHead    string           `json:"base_ip_head"`
	Text          string           `json:"text"`
	OperationID   string           `json:"operation_id"`
	AcceptPartial bool             `json:"accept_partial"`
	Closed        bool             `json:"closed"`
}

func (d ImportDraftContent) Validate() error {
	if d.Policy != nil && d.Policy.Validate() != nil {
		return ErrValidation
	}
	if d.BaseIPHead != "" && !patchHex(d.BaseIPHead, 32) || !utf8.ValidString(d.Text) || len(d.Text) > 256*1024 || !utf8.ValidString(d.OperationID) || strings.TrimSpace(d.OperationID) == "" || utf8.RuneCountInString(d.OperationID) > 100 {
		return ErrValidation
	}
	return nil
}
