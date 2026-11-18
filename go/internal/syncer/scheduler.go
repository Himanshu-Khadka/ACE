package syncer

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/Himanshu-Khadka/ACE/go/internal/store"
)

// Scheduler runs a sync right away and then every Interval. Trigger() starts
// one early (the api's POST /sync uses it). Only one sync runs at a time.
type Scheduler struct {
	Syncer     *Syncer
	Interval   time.Duration
	WebhookURL string

	trigger chan struct{}
	mu      sync.Mutex
	running bool
	nextRun time.Time
}

func NewScheduler(s *Syncer, interval time.Duration, webhook string) *Scheduler {
	return &Scheduler{Syncer: s, Interval: interval, WebhookURL: webhook, trigger: make(chan struct{}, 1)}
}

// Trigger asks for a sync now. Returns false if one is already queued.
func (s *Scheduler) Trigger() bool {
	select {
	case s.trigger <- struct{}{}:
		return true
	default:
		return false
	}
}

type State struct {
	Running  bool      `json:"running"`
	NextRun  time.Time `json:"next_run"`
	Interval string    `json:"interval"`
}

func (s *Scheduler) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return State{Running: s.running, NextRun: s.nextRun, Interval: s.Interval.String()}
}

func (s *Scheduler) Run(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-s.trigger:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		}
		s.RunOnce(ctx)
		timer.Reset(s.Interval)
		s.mu.Lock()
		s.nextRun = time.Now().Add(s.Interval)
		s.mu.Unlock()
	}
}

// RunOnce does one sync, saves it in sync_runs and sends the webhook.
func (s *Scheduler) RunOnce(ctx context.Context) {
	s.mu.Lock()
	s.running = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()

	log := s.Syncer.Log
	if log == nil {
		log = slog.Default()
	}
	st := s.Syncer.Store
	runID, err := st.StartRun(ctx)
	if err != nil {
		log.Error("starting sync", "err", err)
		return
	}
	start := time.Now()
	res, err := s.Syncer.Run(ctx)
	if ctx.Err() != nil {
		err = ctx.Err()
		ctx = context.Background() // still want to save that it was cancelled
	}
	if ferr := st.FinishRun(ctx, runID, err, res, len(res.Changes)); ferr != nil {
		log.Error("saving sync run", "err", ferr)
	}
	if err != nil {
		log.Error("sync failed", "err", err, "took", time.Since(start).Round(time.Second))
		return
	}
	log.Info("sync done", "took", time.Since(start).Round(time.Second), "synced", res.Synced,
		"changes", len(res.Changes), "errors", len(res.Errors), "first_run", res.Baseline)
	if s.WebhookURL != "" && len(res.Changes) > 0 {
		if err := postWebhook(ctx, s.WebhookURL, res.Changes); err != nil {
			log.Warn("webhook failed", "err", err)
		}
	}
}

// postWebhook sends {"changes": [...]} to WEBHOOK_URL so an agent (or n8n,
// discord, whatever) can react without polling.
func postWebhook(ctx context.Context, url string, changes []*store.Change) error {
	body, _ := json.Marshal(map[string]any{"changes": changes})
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return &httpError{resp.StatusCode}
	}
	return nil
}

type httpError struct{ code int }

func (e *httpError) Error() string { return "got HTTP " + http.StatusText(e.code) }
