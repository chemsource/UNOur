// Package audit defines bounded privileged operation records. Credentials never belong here.
package audit

import (
	"context"
	"time"
)

type Entry struct {
	ID     int64     `json:"id"`
	At     time.Time `json:"at"`
	Action string    `json:"action"`
	Target string    `json:"target"`
	Detail string    `json:"detail"`
}
type Page struct {
	Entries []Entry `json:"entries"`
	Total   int     `json:"total"`
}
type Reader interface {
	AuditLog(context.Context, int, int) (Page, error)
}
