package application

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/LordFoxFairy/kokoro-scheduler/internal/domain"
	"github.com/LordFoxFairy/kokoro-scheduler/internal/ports"
)

var (
	ErrInvalidDependencies = errors.New("scheduler application dependencies are incomplete")
	ErrInvalidCommand      = errors.New("scheduler command is invalid")
	ErrIdempotencyConflict = errors.New("idempotency key already used with a different request")
	digestPattern          = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

type Command struct {
	Operation      domain.CommandOperation
	TenantID       string
	Name           string
	CommandScope   string
	IdempotencyKey string
	RequestDigest  string
	RequestID      string
	Schedule       domain.Schedule
}

type Service struct {
	store      ports.Store
	clock      ports.Clock
	recurrence ports.Recurrence
}

func NewService(store ports.Store, clock ports.Clock, recurrence ports.Recurrence) (*Service, error) {
	if store == nil || clock == nil || recurrence == nil {
		return nil, ErrInvalidDependencies
	}
	return &Service{store: store, clock: clock, recurrence: recurrence}, nil
}

func (s *Service) Execute(ctx context.Context, command Command) (domain.CommandReceipt, error) {
	prepared, err := s.prepare(command)
	if err != nil {
		return domain.CommandReceipt{}, err
	}
	var receipt domain.CommandReceipt
	err = s.store.WithinTx(ctx, func(tx ports.TxStore) error {
		if err := tx.LockCommandIdentity(ctx, prepared.TenantID, prepared.CommandScope, prepared.IdempotencyKey); err != nil {
			return err
		}
		prior, found, err := tx.FindCommandReceipt(ctx, prepared.TenantID, prepared.CommandScope, prepared.IdempotencyKey)
		if err != nil {
			return err
		}
		if found {
			if prior.RequestDigest != prepared.RequestDigest {
				return ErrIdempotencyConflict
			}
			receipt = prior
			return nil
		}

		result, err := s.apply(ctx, tx, prepared)
		if err != nil {
			return err
		}
		receipt = domain.CommandReceipt{
			TenantID:       prepared.TenantID,
			CommandScope:   prepared.CommandScope,
			IdempotencyKey: prepared.IdempotencyKey,
			RequestDigest:  prepared.RequestDigest,
			RequestID:      prepared.RequestID,
			Result:         result,
			CreatedAt:      domain.NormalizeInstant(s.clock.Now()),
		}
		return tx.InsertCommandReceipt(ctx, receipt)
	})
	if err != nil {
		return domain.CommandReceipt{}, err
	}
	return receipt, nil
}

func (s *Service) prepare(command Command) (Command, error) {
	command.TenantID = strings.TrimSpace(command.TenantID)
	command.Name = strings.TrimSpace(command.Name)
	command.CommandScope = strings.TrimSpace(command.CommandScope)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.RequestID = strings.TrimSpace(command.RequestID)
	if err := domain.ValidateTenantID(command.TenantID); err != nil {
		return Command{}, errors.Join(ErrInvalidCommand, err)
	}
	if !domain.IsValidScheduleName(command.Name) || command.CommandScope == "" || len(command.CommandScope) > 256 || command.IdempotencyKey == "" || len(command.IdempotencyKey) > 256 || command.RequestID == "" || len(command.RequestID) > 128 || !digestPattern.MatchString(command.RequestDigest) {
		return Command{}, ErrInvalidCommand
	}
	switch command.Operation {
	case domain.CommandCreate, domain.CommandUpdate:
		command.Schedule.TenantID = command.TenantID
		command.Schedule.Name = command.Name
		normalized, err := command.Schedule.Normalized()
		if err != nil {
			return Command{}, errors.Join(ErrInvalidCommand, err)
		}
		if err := s.recurrence.Validate(normalized.Rule, normalized.Timezone); err != nil {
			return Command{}, errors.Join(ErrInvalidCommand, err)
		}
		now := domain.NormalizeInstant(s.clock.Now())
		normalized.NextDueAt, err = s.recurrence.Next(normalized.Rule, normalized.Timezone, now)
		if err != nil {
			return Command{}, errors.Join(ErrInvalidCommand, err)
		}
		normalized.CreatedAt = now
		normalized.UpdatedAt = now
		command.Schedule = normalized
	case domain.CommandDelete, domain.CommandPause, domain.CommandResume:
	default:
		return Command{}, ErrInvalidCommand
	}
	return command, nil
}

func (s *Service) apply(ctx context.Context, tx ports.TxStore, command Command) (domain.CommandResult, error) {
	now := domain.NormalizeInstant(s.clock.Now())
	switch command.Operation {
	case domain.CommandCreate:
		created, err := tx.CreateSchedule(ctx, command.Schedule)
		if errors.Is(err, domain.ErrScheduleAlreadyExists) {
			return domain.CommandResult{Code: domain.ResultAlreadyExists, Name: command.Name}, nil
		}
		return scheduleResult(domain.ResultRegistered, created, err)
	case domain.CommandUpdate:
		updated, err := tx.UpdateSchedule(ctx, command.Schedule)
		if errors.Is(err, domain.ErrScheduleNotFound) {
			return domain.CommandResult{Code: domain.ResultNotFound, Name: command.Name}, nil
		}
		return scheduleResult(domain.ResultUpdated, updated, err)
	case domain.CommandDelete:
		err := tx.DeleteSchedule(ctx, command.TenantID, command.Name)
		if errors.Is(err, domain.ErrScheduleNotFound) {
			return domain.CommandResult{Code: domain.ResultNotFound, Name: command.Name}, nil
		}
		return domain.CommandResult{Code: domain.ResultDeleted, Name: command.Name}, err
	case domain.CommandPause:
		paused, err := tx.SetScheduleStatus(ctx, command.TenantID, command.Name, domain.SchedulePaused, time.Time{}, now)
		if errors.Is(err, domain.ErrScheduleNotFound) {
			return domain.CommandResult{Code: domain.ResultNotFound, Name: command.Name}, nil
		}
		return scheduleResult(domain.ResultPaused, paused, err)
	case domain.CommandResume:
		current, err := tx.GetSchedule(ctx, command.TenantID, command.Name)
		if errors.Is(err, domain.ErrScheduleNotFound) {
			return domain.CommandResult{Code: domain.ResultNotFound, Name: command.Name}, nil
		}
		if err != nil {
			return domain.CommandResult{}, err
		}
		nextDueAt, err := s.recurrence.Next(current.Rule, current.Timezone, now)
		if err != nil {
			return domain.CommandResult{}, err
		}
		resumed, err := tx.SetScheduleStatus(ctx, command.TenantID, command.Name, domain.ScheduleActive, nextDueAt, now)
		return scheduleResult(domain.ResultResumed, resumed, err)
	default:
		return domain.CommandResult{}, ErrInvalidCommand
	}
}

func scheduleResult(code string, schedule domain.Schedule, err error) (domain.CommandResult, error) {
	if err != nil {
		return domain.CommandResult{}, err
	}
	return domain.CommandResult{Code: code, Name: schedule.Name, Schedule: &schedule}, nil
}
