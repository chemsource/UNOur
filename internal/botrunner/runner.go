// Package botrunner owns bounded background computer work. Room services own
// authorization, deadlines and commits; the brain never owns a live aggregate.
package botrunner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"unour/game"
	"unour/internal/bot"
	"unour/internal/room"
)

type Queue interface {
	DueBots(context.Context, time.Time, int) ([]string, error)
}
type Rooms interface {
	NextBotTask(context.Context, string) (*room.BotTask, error)
	ApplyBot(context.Context, string, room.BotTask, game.Command) error
	RetryBot(context.Context, string, room.BotTask, time.Duration) error
}
type Thinker interface {
	Decide(context.Context, bot.Observation, bot.Profile, bot.SearchConfig) (bot.Decision, error)
}

type Config struct {
	Workers    int
	Interval   time.Duration
	RetryDelay time.Duration
	Now        func() time.Time
	Logger     *slog.Logger
}
type Runner struct {
	queue Queue
	rooms Rooms
	brain Thinker
	cfg   Config
}

func New(queue Queue, rooms Rooms, brain Thinker, cfg Config) (*Runner, error) {
	if queue == nil || rooms == nil || brain == nil {
		return nil, fmt.Errorf("computer queue, rooms and thinker are required")
	}
	if cfg.Workers == 0 {
		cfg.Workers = 2
	}
	if cfg.Interval == 0 {
		cfg.Interval = 100 * time.Millisecond
	}
	if cfg.RetryDelay == 0 {
		cfg.RetryDelay = 2 * time.Second
	}
	if cfg.Workers < 1 || cfg.Workers > 8 || cfg.Interval < 10*time.Millisecond || cfg.Interval > time.Second || cfg.RetryDelay < 100*time.Millisecond || cfg.RetryDelay > 5*time.Second {
		return nil, fmt.Errorf("invalid computer worker limits")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Runner{queue: queue, rooms: rooms, brain: brain, cfg: cfg}, nil
}

// Run blocks until cancellation, then cancels and joins every worker before
// returning. One Runner is started once by the server's composition root.
func (r *Runner) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	jobs := make(chan string, r.cfg.Workers)
	completed := make(chan string, r.cfg.Workers)
	var workers sync.WaitGroup
	for i := 0; i < r.cfg.Workers; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case id := <-jobs:
					if err := r.step(ctx, id); !expected(err) && ctx.Err() == nil {
						r.cfg.Logger.Error("computer job failed", "room", id, "error", err)
					}
					select {
					case completed <- id:
					case <-ctx.Done():
						return
					}
				}
			}
		}()
	}
	defer func() { cancel(); workers.Wait() }()
	ticker := time.NewTicker(r.cfg.Interval)
	defer ticker.Stop()
	inFlight := make(map[string]bool, r.cfg.Workers)
	var queryAfter time.Time
	dispatch := func() {
		if len(inFlight) >= r.cfg.Workers || r.cfg.Now().Before(queryAfter) {
			return
		}
		ids, err := r.queue.DueBots(ctx, r.cfg.Now(), 64)
		if err != nil {
			if ctx.Err() == nil {
				r.cfg.Logger.Error("read computer queue", "error", err)
			}
			queryAfter = r.cfg.Now().Add(time.Second)
			return
		}
		for _, id := range ids {
			if inFlight[id] {
				continue
			}
			if len(inFlight) >= r.cfg.Workers {
				break
			}
			inFlight[id] = true
			select {
			case jobs <- id:
			case <-ctx.Done():
				return
			}
		}
	}
	dispatch()
	for {
		select {
		case <-ctx.Done():
			return nil
		case id := <-completed:
			delete(inFlight, id)
		case <-ticker.C:
			dispatch()
		}
	}
}

func expected(err error) bool {
	return err == nil || errors.Is(err, room.ErrConflict) || errors.Is(err, room.ErrNotFound) || errors.Is(err, context.Canceled)
}

func (r *Runner) step(ctx context.Context, id string) (err error) {
	var task *room.BotTask
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("computer worker panic: %v", recovered)
		}
		if !expected(err) && ctx.Err() == nil && task != nil {
			if retryErr := r.rooms.RetryBot(ctx, id, *task, r.cfg.RetryDelay); !expected(retryErr) {
				err = errors.Join(err, retryErr)
			}
		}
	}()
	task, err = r.rooms.NextBotTask(ctx, id)
	if err != nil || task == nil {
		return err
	}
	cmd := game.Command{PlayerID: task.PlayerID, Type: task.Type, WindowID: task.WindowID}
	if task.Type == "" {
		searchCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		decision, thinkErr := r.brain.Decide(searchCtx, task.Observation, task.Profile, bot.SearchConfig{})
		if thinkErr != nil {
			return thinkErr
		}
		cmd = decision.Command
	}
	return r.rooms.ApplyBot(ctx, id, *task, cmd)
}
