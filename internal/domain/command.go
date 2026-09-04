package domain

import "time"

type CommandOperation string

const (
	CommandCreate CommandOperation = "create"
	CommandUpdate CommandOperation = "update"
	CommandDelete CommandOperation = "delete"
	CommandPause  CommandOperation = "pause"
	CommandResume CommandOperation = "resume"

	ResultRegistered    = "registered"
	ResultUpdated       = "updated"
	ResultDeleted       = "deleted"
	ResultPaused        = "paused"
	ResultResumed       = "resumed"
	ResultAlreadyExists = "schedule_already_exists"
	ResultNotFound      = "schedule_not_found"
)

type CommandResult struct {
	Code     string    `json:"code"`
	Name     string    `json:"name"`
	Schedule *Schedule `json:"schedule,omitempty"`
}

type CommandReceipt struct {
	TenantID       string
	CommandScope   string
	IdempotencyKey string
	RequestDigest  string
	RequestID      string
	Result         CommandResult
	CreatedAt      time.Time
}
