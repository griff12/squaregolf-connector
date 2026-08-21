package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/brentyates/squaregolf-connector/internal/core/protocol"
)

const defaultShotHistoryLimit = 100

// Shot is the canonical record that correlates launch-monitor data with every
// contribution produced by external integrations.
type Shot struct {
	ID         string                   `json:"id"`
	Sequence   uint64                   `json:"sequence"`
	OccurredAt time.Time                `json:"occurredAt"`
	UpdatedAt  time.Time                `json:"updatedAt"`
	Ball       *protocol.BallMetrics    `json:"ball,omitempty"`
	Club       *protocol.ClubMetrics    `json:"club,omitempty"`
	ClubType   *protocol.ClubType       `json:"clubType,omitempty"`
	ClubName   string                   `json:"clubName,omitempty"`
	Handedness *protocol.HandednessType `json:"handedness,omitempty"`
	Results    []Result                 `json:"results"`
}

// Metric is a scalar measurement contributed by a plugin.
type Metric struct {
	Key       string   `json:"key"`
	Label     string   `json:"label"`
	Value     float64  `json:"value"`
	Unit      string   `json:"unit,omitempty"`
	Phase     string   `json:"phase,omitempty"`
	Status    string   `json:"status,omitempty"`
	TargetMin *float64 `json:"targetMin,omitempty"`
	TargetMax *float64 `json:"targetMax,omitempty"`
}

// Insight is an interpretation or recommendation derived from plugin data.
type Insight struct {
	Key            string `json:"key"`
	Title          string `json:"title"`
	Message        string `json:"message"`
	Severity       string `json:"severity,omitempty"`
	Phase          string `json:"phase,omitempty"`
	Recommendation string `json:"recommendation,omitempty"`
}

// Media references an artifact owned by an integration, such as a swing video.
type Media struct {
	ID           string `json:"id,omitempty"`
	Type         string `json:"type"` // video | image
	Label        string `json:"label,omitempty"`
	URL          string `json:"url"`
	ThumbnailURL string `json:"thumbnailUrl,omitempty"`
}

// Series is a chart-ready sequence, such as wrist flexion through the swing.
type Series struct {
	Key    string    `json:"key"`
	Label  string    `json:"label"`
	Unit   string    `json:"unit,omitempty"`
	Points []float64 `json:"points"`
}

// Link lets an integration hand off to a richer standalone experience.
type Link struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// Result is a versioned contribution from one plugin to one canonical shot.
// Data is reserved for versioned plugin-specific details that generic renderers
// do not understand; the common fields above drive the unified shot UI.
type Result struct {
	ID            string          `json:"id"`
	Plugin        string          `json:"plugin"`
	Kind          string          `json:"kind"`
	SchemaVersion int             `json:"schemaVersion"`
	CorrelationID string          `json:"correlationId"`
	CreatedAt     time.Time       `json:"createdAt"`
	Summary       string          `json:"summary,omitempty"`
	Metrics       []Metric        `json:"metrics,omitempty"`
	Insights      []Insight       `json:"insights,omitempty"`
	Media         []Media         `json:"media,omitempty"`
	Series        []Series        `json:"series,omitempty"`
	Links         []Link          `json:"links,omitempty"`
	Data          json.RawMessage `json:"data,omitempty"`
}

type ShotEventKind string

const (
	ShotCreated     ShotEventKind = "shot.created"
	ShotUpdated     ShotEventKind = "shot.updated"
	ShotResultAdded ShotEventKind = "shot.result_added"
)

type ShotEvent struct {
	Kind ShotEventKind `json:"kind"`
	Shot Shot          `json:"shot"`
}

// Subscription releases a callback registered with the timeline or host.
type Subscription interface {
	Close()
}

type subscription struct {
	once    sync.Once
	closeFn func()
}

func (s *subscription) Close() {
	s.once.Do(s.closeFn)
}

type timelineSubscriber struct {
	mu     sync.Mutex
	cond   *sync.Cond
	queue  []ShotEvent
	closed bool
	fn     func(ShotEvent)
}

func newTimelineSubscriber(fn func(ShotEvent)) *timelineSubscriber {
	subscriber := &timelineSubscriber{fn: fn}
	subscriber.cond = sync.NewCond(&subscriber.mu)
	go subscriber.run()
	return subscriber
}

func (s *timelineSubscriber) enqueue(event ShotEvent) {
	s.mu.Lock()
	if !s.closed {
		s.queue = append(s.queue, event)
		s.cond.Signal()
	}
	s.mu.Unlock()
}

func (s *timelineSubscriber) close() {
	s.mu.Lock()
	s.closed = true
	s.queue = nil
	s.cond.Broadcast()
	s.mu.Unlock()
}

func (s *timelineSubscriber) run() {
	for {
		s.mu.Lock()
		for !s.closed && len(s.queue) == 0 {
			s.cond.Wait()
		}
		if s.closed {
			s.mu.Unlock()
			return
		}
		event := s.queue[0]
		s.queue[0] = ShotEvent{}
		s.queue = s.queue[1:]
		s.mu.Unlock()

		func() {
			defer func() { _ = recover() }()
			s.fn(event)
		}()
	}
}

// Timeline owns bounded in-memory shot history and plugin contributions.
// Persistence can be added behind this API without changing plugin contracts.
type Timeline struct {
	mu           sync.RWMutex
	shots        []*Shot
	byID         map[string]*Shot
	nextShot     uint64
	nextResult   uint64
	nextSub      uint64
	subscribers  map[uint64]*timelineSubscriber
	historyLimit int
	now          func() time.Time
}

func NewTimeline() *Timeline {
	return &Timeline{
		byID:         make(map[string]*Shot),
		subscribers:  make(map[uint64]*timelineSubscriber),
		historyLimit: defaultShotHistoryLimit,
		now:          time.Now,
	}
}

func (t *Timeline) RecordShot(ball *protocol.BallMetrics, clubType *protocol.ClubType, clubName string, handedness *protocol.HandednessType) Shot {
	now := t.now().UTC()
	t.mu.Lock()
	t.nextShot++
	shot := &Shot{
		ID:         fmt.Sprintf("shot-%d-%d", now.UnixMilli(), t.nextShot),
		Sequence:   t.nextShot,
		OccurredAt: now,
		UpdatedAt:  now,
		Ball:       cloneBall(ball),
		ClubType:   cloneClubType(clubType),
		ClubName:   clubName,
		Handedness: cloneHandedness(handedness),
		Results:    []Result{},
	}
	t.shots = append(t.shots, shot)
	t.byID[shot.ID] = shot
	if len(t.shots) > t.historyLimit {
		oldest := t.shots[0]
		delete(t.byID, oldest.ID)
		t.shots = t.shots[1:]
	}
	event, callbacks := t.eventLocked(ShotCreated, shot)
	t.mu.Unlock()
	notifyTimeline(callbacks, event)
	return event.Shot
}

// UpdateLatestClub attaches the launch monitor's later club-metrics response to
// the most recently recorded shot.
func (t *Timeline) UpdateLatestClub(club *protocol.ClubMetrics) (Shot, bool) {
	if club == nil {
		return Shot{}, false
	}
	t.mu.Lock()
	if len(t.shots) == 0 {
		t.mu.Unlock()
		return Shot{}, false
	}
	shot := t.shots[len(t.shots)-1]
	shot.Club = cloneClub(club)
	shot.UpdatedAt = t.now().UTC()
	event, callbacks := t.eventLocked(ShotUpdated, shot)
	t.mu.Unlock()
	notifyTimeline(callbacks, event)
	return event.Shot, true
}

func (t *Timeline) PublishResult(result Result) (Shot, error) {
	if result.Plugin == "" {
		return Shot{}, errors.New("plugin result requires a plugin name")
	}
	if result.Kind == "" {
		return Shot{}, errors.New("plugin result requires a kind")
	}
	if result.CorrelationID == "" {
		return Shot{}, errors.New("plugin result requires a shot correlation ID")
	}
	if len(result.Data) > 0 && !json.Valid(result.Data) {
		return Shot{}, errors.New("plugin result data must contain valid JSON")
	}
	if err := validateResult(result); err != nil {
		return Shot{}, err
	}

	t.mu.Lock()
	shot, ok := t.byID[result.CorrelationID]
	if !ok {
		t.mu.Unlock()
		return Shot{}, fmt.Errorf("correlated shot %q was not found", result.CorrelationID)
	}
	t.nextResult++
	now := t.now().UTC()
	if result.ID == "" {
		result.ID = fmt.Sprintf("result-%d-%d", now.UnixMilli(), t.nextResult)
	}
	if result.SchemaVersion == 0 {
		result.SchemaVersion = 1
	}
	if result.CreatedAt.IsZero() {
		result.CreatedAt = now
	}
	result = cloneResult(result)
	shot.Results = append(shot.Results, result)
	shot.UpdatedAt = now
	event, callbacks := t.eventLocked(ShotResultAdded, shot)
	t.mu.Unlock()
	notifyTimeline(callbacks, event)
	return event.Shot, nil
}

func validateResult(result Result) error {
	for _, metric := range result.Metrics {
		if metric.Key == "" || metric.Label == "" {
			return errors.New("plugin metrics require a key and label")
		}
	}
	for _, insight := range result.Insights {
		if insight.Key == "" || insight.Title == "" || insight.Message == "" {
			return errors.New("plugin insights require a key, title, and message")
		}
	}
	for _, media := range result.Media {
		if (media.Type != "video" && media.Type != "image") || media.URL == "" {
			return errors.New("plugin media require an image or video type and URL")
		}
	}
	for _, series := range result.Series {
		if series.Key == "" || series.Label == "" {
			return errors.New("plugin series require a key and label")
		}
	}
	for _, link := range result.Links {
		if link.Label == "" || link.URL == "" {
			return errors.New("plugin links require a label and URL")
		}
	}
	return nil
}

// Shots returns newest-first snapshots, bounded by limit when it is positive.
func (t *Timeline) Shots(limit int) []Shot {
	t.mu.RLock()
	defer t.mu.RUnlock()
	count := len(t.shots)
	if limit > 0 && limit < count {
		count = limit
	}
	out := make([]Shot, 0, count)
	for i := len(t.shots) - 1; i >= len(t.shots)-count; i-- {
		out = append(out, cloneShot(t.shots[i]))
	}
	return out
}

func (t *Timeline) Shot(id string) (Shot, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	shot, ok := t.byID[id]
	if !ok {
		return Shot{}, false
	}
	return cloneShot(shot), true
}

func (t *Timeline) Subscribe(fn func(ShotEvent)) Subscription {
	subscriber := newTimelineSubscriber(fn)
	t.mu.Lock()
	t.nextSub++
	id := t.nextSub
	t.subscribers[id] = subscriber
	t.mu.Unlock()
	return &subscription{closeFn: func() {
		t.mu.Lock()
		delete(t.subscribers, id)
		t.mu.Unlock()
		subscriber.close()
	}}
}

func (t *Timeline) eventLocked(kind ShotEventKind, shot *Shot) (ShotEvent, []*timelineSubscriber) {
	event := ShotEvent{Kind: kind, Shot: cloneShot(shot)}
	subscribers := make([]*timelineSubscriber, 0, len(t.subscribers))
	for _, subscriber := range t.subscribers {
		subscribers = append(subscribers, subscriber)
	}
	return event, subscribers
}

func notifyTimeline(subscribers []*timelineSubscriber, event ShotEvent) {
	for _, subscriber := range subscribers {
		subscriber.enqueue(event)
	}
}

func cloneShot(value *Shot) Shot {
	shot := *value
	shot.Ball = cloneBall(value.Ball)
	shot.Club = cloneClub(value.Club)
	shot.ClubType = cloneClubType(value.ClubType)
	shot.Handedness = cloneHandedness(value.Handedness)
	shot.Results = make([]Result, len(value.Results))
	for i, result := range value.Results {
		shot.Results[i] = cloneResult(result)
	}
	return shot
}

func cloneBall(value *protocol.BallMetrics) *protocol.BallMetrics {
	if value == nil {
		return nil
	}
	copy := *value
	copy.RawData = append([]string(nil), value.RawData...)
	return &copy
}

func cloneClub(value *protocol.ClubMetrics) *protocol.ClubMetrics {
	if value == nil {
		return nil
	}
	copy := *value
	copy.RawData = append([]string(nil), value.RawData...)
	return &copy
}

func cloneClubType(value *protocol.ClubType) *protocol.ClubType {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneHandedness(value *protocol.HandednessType) *protocol.HandednessType {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneResult(value Result) Result {
	value.Metrics = append([]Metric(nil), value.Metrics...)
	value.Insights = append([]Insight(nil), value.Insights...)
	value.Media = append([]Media(nil), value.Media...)
	value.Links = append([]Link(nil), value.Links...)
	value.Data = append(json.RawMessage(nil), value.Data...)
	value.Series = append([]Series(nil), value.Series...)
	for i := range value.Series {
		value.Series[i].Points = append([]float64(nil), value.Series[i].Points...)
	}
	return value
}
